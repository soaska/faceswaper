/// <reference path="../pb_data/types.d.ts" />

// Atomically returns the oldest available task and marks it as processing.
routerAdd(
  "POST",
  "/api/faceswaper/jobs/claim",
  (c) => {
    const info = $apis.requestInfo(c);
    const collection = String(info.data.collection || "");
    const workerId = String(info.data.worker_id || "");
    if (collection !== "circle_jobs" && collection !== "face_jobs") {
      throw new BadRequestError("Неизвестная коллекция задач.");
    }
    if (!/^[a-zA-Z0-9._-]{1,100}$/.test(workerId)) {
      throw new BadRequestError("Некорректный идентификатор воркера.");
    }

    const now = new Date();
    const nowValue = now.toISOString().replace("T", " ");
    const leaseValue = new Date(now.getTime() + 15 * 60 * 1000)
      .toISOString()
      .replace("T", " ");
    let claimed = null;

    $app.dao().runInTransaction((txDao) => {
      for (const name of ["circle_jobs", "face_jobs"]) {
        const expired = txDao.findRecordsByFilter(
          name,
          "(status = 'processing' || status = 'sending') && lease_until != '' && lease_until < '" +
            nowValue +
            "'",
          "created",
          20,
          0,
        );
        for (const record of expired) {
          if (record.getInt("attempts") >= 3) {
            record.set("status", "error: превышено число попыток обработки");
          } else {
            record.set("status", "queued");
          }
          record.set("claimed_by", "");
          record.set("lease_until", "");
          txDao.saveRecord(record);
        }
      }

      const candidates = txDao.findRecordsByFilter(
        collection,
        "status = 'queued' && attempts < 3",
        "created",
        20,
        0,
      );
      for (const record of candidates) {
        const owner = record.getString("owner");
        let ownerBusy = false;
        for (const name of ["circle_jobs", "face_jobs"]) {
          const active = txDao.findRecordsByFilter(
            name,
            "owner = '" + owner + "' && (status = 'processing' || status = 'sending')",
            "created",
            1,
            0,
          );
          if (active.length > 0) {
            ownerBusy = true;
            break;
          }
        }
        if (ownerBusy) {
          continue;
        }

        record.set("status", "processing");
        record.set("claimed_by", workerId);
        record.set("lease_until", leaseValue);
        record.set("attempts", record.getInt("attempts") + 1);
        txDao.saveRecord(record);
        claimed = record;
        break;
      }
    });

    if (!claimed) {
      return c.json(200, { task: null });
    }
    return c.json(200, {
      task: {
        id: claimed.id,
        owner: claimed.getString("owner"),
        input_media: claimed.getString("input_media"),
        input_face: claimed.getString("input_face"),
        output_media: claimed.getString("output_media"),
        status: claimed.getString("status"),
        attempts: claimed.getInt("attempts"),
      },
    });
  },
  $apis.requireAdminAuth(),
);

// Extends the lease while a worker is processing or sending a task.
routerAdd(
  "POST",
  "/api/faceswaper/jobs/heartbeat",
  (c) => {
    const info = $apis.requestInfo(c);
    const collection = String(info.data.collection || "");
    const taskId = String(info.data.task_id || "");
    const workerId = String(info.data.worker_id || "");
    if (collection !== "circle_jobs" && collection !== "face_jobs") {
      throw new BadRequestError("Неизвестная коллекция задач.");
    }

    $app.dao().runInTransaction((txDao) => {
      const record = txDao.findRecordById(collection, taskId);
      const status = record.getString("status");
      if (record.getString("claimed_by") !== workerId) {
        throw new ForbiddenError("Задача принадлежит другому воркеру.");
      }
      if (status !== "processing" && status !== "sending") {
        throw new BadRequestError("Нельзя продлить завершённую задачу.");
      }
      const leaseValue = new Date(Date.now() + 15 * 60 * 1000)
        .toISOString()
        .replace("T", " ");
      record.set("lease_until", leaseValue);
      txDao.saveRecord(record);
    });

    return c.json(200, { ok: true });
  },
  $apis.requireAdminAuth(),
);

// Couples each user balance/counter change to its job state transition in one
// transaction. Every action is idempotent so a lost HTTP response is safe to
// retry with the same job ID.
routerAdd(
  "POST",
  "/api/faceswaper/jobs/settle",
  (c) => {
    const info = $apis.requestInfo(c);
    const collection = String(info.data.collection || "");
    const taskId = String(info.data.task_id || "");
    const workerId = String(info.data.worker_id || "");
    const action = String(info.data.action || "");
    const requestedPrice = Number(info.data.price || 0);
    const duration = Number(info.data.duration || 0);
    const threads = Number(info.data.threads || 0);
    const errorMessage = String(info.data.error || "").trim();

    if (collection !== "circle_jobs" && collection !== "face_jobs") {
      throw new BadRequestError("Неизвестная коллекция задач.");
    }
    if (!/^[a-zA-Z0-9_-]{1,30}$/.test(taskId)) {
      throw new BadRequestError("Некорректный идентификатор задачи.");
    }
    if (!/^[a-zA-Z0-9._-]{1,100}$/.test(workerId)) {
      throw new BadRequestError("Некорректный идентификатор воркера.");
    }
    if (["start_sending", "complete", "fail"].indexOf(action) === -1) {
      throw new BadRequestError("Некорректное действие задачи.");
    }
    if (
      !Number.isInteger(requestedPrice) ||
      !Number.isInteger(duration) ||
      !Number.isInteger(threads) ||
      requestedPrice < 0 ||
      duration < 0 ||
      threads < 0
    ) {
      throw new BadRequestError("Цена и параметры задачи должны быть неотрицательными целыми числами.");
    }

    // PocketBase 0.22 evaluates route callbacks in an isolated JS context, so
    // callback helpers must be declared inside the callback itself.
    function findOperation(txDao, operationKey) {
      const records = txDao.findRecordsByFilter(
        "user_operations",
        "operation_key = '" + operationKey + "'",
        "",
        1,
        0,
      );
      return records.length > 0 ? records[0] : null;
    }

    function applyOperation(txDao, data) {
      const existing = findOperation(txDao, data.operationKey);
      if (existing) {
        return existing;
      }

      const user = txDao.findRecordById("users", data.userId);
      const balance = user.getInt("coins") + data.coinsDelta;
      if (balance < 0) {
        throw new BadRequestError(
          "Недостаточно монет. Требуется: " +
            -data.coinsDelta +
            ", доступно: " +
            user.getInt("coins"),
        );
      }

      user.set("coins", balance);
      user.set("circle_count", user.getInt("circle_count") + data.circleDelta);
      user.set("face_replace_count", user.getInt("face_replace_count") + data.faceDelta);
      txDao.saveRecord(user);

      const operation = new Record(txDao.findCollectionByNameOrId("user_operations"));
      operation.set("operation_key", data.operationKey);
      operation.set("user", data.userId);
      operation.set("job_id", data.jobId);
      operation.set("kind", data.kind);
      operation.set("coins_delta", data.coinsDelta);
      operation.set("circle_delta", data.circleDelta);
      operation.set("face_delta", data.faceDelta);
      operation.set("balance_after", balance);
      txDao.saveRecord(operation);
      return operation;
    }

    let result = null;
    $app.dao().runInTransaction((txDao) => {
      const record = txDao.findRecordById(collection, taskId);
      const currentStatus = record.getString("status");
      const scope = collection === "circle_jobs" ? "circle" : "face";
      const owner = record.getString("owner");
      const chargeKey = scope + ":" + taskId + ":charge";
      const refundKey = scope + ":" + taskId + ":refund";
      const completeKey = scope + ":" + taskId + ":complete";

      if (action === "start_sending") {
        if (currentStatus === "sending") {
          if (record.getString("claimed_by") !== workerId) {
            throw new ForbiddenError("Задача принадлежит другому воркеру.");
          }
          const existingCharge = findOperation(txDao, chargeKey);
          if (!existingCharge) {
            throw new BadRequestError("Для sending-задачи не найдено списание.");
          }
          result = {
            ok: true,
            already: true,
            price: -existingCharge.getInt("coins_delta"),
            balance: existingCharge.getInt("balance_after"),
          };
          return;
        }
        if (currentStatus !== "processing" || record.getString("claimed_by") !== workerId) {
          throw new ForbiddenError("Задача не принадлежит активному воркеру.");
        }
        if (requestedPrice < 1) {
          throw new BadRequestError("Цена задачи должна быть положительной.");
        }
        let charge = findOperation(txDao, chargeKey);
        if (!charge) {
          charge = applyOperation(txDao, {
            operationKey: chargeKey,
            userId: owner,
            jobId: taskId,
            kind: scope + "_charge",
            coinsDelta: -requestedPrice,
            circleDelta: 0,
            faceDelta: 0,
          });
        }
        const actualPrice = -charge.getInt("coins_delta");
        if (collection === "face_jobs") {
          record.set("duration", duration);
          record.set("threads", threads);
          record.set("price", actualPrice);
        }
        record.set("status", "sending");
        txDao.saveRecord(record);
        result = {
          ok: true,
          already: false,
          price: actualPrice,
          balance: charge.getInt("balance_after"),
        };
        return;
      }

      if (action === "complete") {
        if (currentStatus === "completed") {
          result = { ok: true, already: true };
          return;
        }
        if (currentStatus !== "sending" || record.getString("claimed_by") !== workerId) {
          throw new ForbiddenError("Задача не принадлежит активному воркеру.");
        }
        applyOperation(txDao, {
          operationKey: completeKey,
          userId: owner,
          jobId: taskId,
          kind: scope + "_complete",
          coinsDelta: 0,
          circleDelta: scope === "circle" ? 1 : 0,
          faceDelta: scope === "face" ? 1 : 0,
        });
        record.set("status", "completed");
        record.set("claimed_by", "");
        record.set("lease_until", "");
        txDao.saveRecord(record);
        result = { ok: true, already: false };
        return;
      }

      if (currentStatus.indexOf("error:") === 0) {
        result = { ok: true, already: true };
        return;
      }
      if (
        (currentStatus !== "processing" && currentStatus !== "sending") ||
        record.getString("claimed_by") !== workerId
      ) {
        throw new ForbiddenError("Задача не принадлежит активному воркеру.");
      }
      const charge = findOperation(txDao, chargeKey);
      if (charge && !findOperation(txDao, refundKey)) {
        applyOperation(txDao, {
          operationKey: refundKey,
          userId: owner,
          jobId: taskId,
          kind: scope + "_refund",
          coinsDelta: -charge.getInt("coins_delta"),
          circleDelta: 0,
          faceDelta: 0,
        });
      }
      record.set("status", "error: " + (errorMessage || "неизвестная ошибка"));
      record.set("claimed_by", "");
      record.set("lease_until", "");
      txDao.saveRecord(record);
      result = { ok: true, already: false, refunded: charge !== null };
    });

    return c.json(200, result);
  },
  $apis.requireAdminAuth(),
);

// Changes task state only when it is still owned by the calling worker.
routerAdd(
  "POST",
  "/api/faceswaper/jobs/status",
  (c) => {
    const info = $apis.requestInfo(c);
    const collection = String(info.data.collection || "");
    const taskId = String(info.data.task_id || "");
    const workerId = String(info.data.worker_id || "");
    const nextStatus = String(info.data.status || "");
    if (collection !== "circle_jobs" && collection !== "face_jobs") {
      throw new BadRequestError("Неизвестная коллекция задач.");
    }

    $app.dao().runInTransaction((txDao) => {
      const record = txDao.findRecordById(collection, taskId);
      const currentStatus = record.getString("status");
      if (record.getString("claimed_by") !== workerId) {
        throw new ForbiddenError("Задача принадлежит другому воркеру.");
      }

      const isError = nextStatus.indexOf("error:") === 0;
      const allowed =
        (currentStatus === "processing" && nextStatus === "sending") ||
        (currentStatus === "sending" && nextStatus === "completed") ||
        ((currentStatus === "processing" || currentStatus === "sending") && isError);
      if (!allowed) {
        throw new BadRequestError(
          "Недопустимый переход статуса: " + currentStatus + " -> " + nextStatus,
        );
      }

      record.set("status", nextStatus);
      if (nextStatus === "completed" || isError) {
        record.set("claimed_by", "");
        record.set("lease_until", "");
      }
      txDao.saveRecord(record);
    });

    return c.json(200, { ok: true });
  },
  $apis.requireAdminAuth(),
);
