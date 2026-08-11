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
