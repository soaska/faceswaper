/// <reference path="../pb_data/types.d.ts" />

// Applies balance and counter changes exactly once under a unique operation key.
routerAdd(
  "POST",
  "/api/faceswaper/users/apply-operation",
  (c) => {
    const info = $apis.requestInfo(c);
    const userId = String(info.data.user_id || "");
    const jobId = String(info.data.job_id || "");
    const kind = String(info.data.kind || "");
    const operationKey = String(info.data.operation_key || "");
    const coinsDelta = Number(info.data.coins_delta || 0);
    const circleDelta = Number(info.data.circle_delta || 0);
    const faceDelta = Number(info.data.face_delta || 0);

    if (!/^[a-zA-Z0-9:_-]{1,100}$/.test(operationKey)) {
      throw new BadRequestError("Некорректный ключ операции.");
    }
    if (!/^[a-zA-Z0-9_-]{1,30}$/.test(jobId) || !/^[a-z_]{1,30}$/.test(kind)) {
      throw new BadRequestError("Некорректные данные операции.");
    }
    if (
      !Number.isInteger(coinsDelta) ||
      !Number.isInteger(circleDelta) ||
      !Number.isInteger(faceDelta) ||
      circleDelta < 0 ||
      circleDelta > 1 ||
      faceDelta < 0 ||
      faceDelta > 1
    ) {
      throw new BadRequestError("Изменения пользователя должны быть целыми числами.");
    }

    let result = null;
    $app.dao().runInTransaction((txDao) => {
      const existing = txDao.findRecordsByFilter(
        "user_operations",
        "operation_key = '" + operationKey + "'",
        "",
        1,
        0,
      );
      if (existing.length > 0) {
        result = {
          applied: false,
          balance: existing[0].getInt("balance_after"),
        };
        return;
      }

      const user = txDao.findRecordById("users", userId);
      const balance = user.getInt("coins") + coinsDelta;
      if (balance < 0) {
        throw new BadRequestError(
          "Недостаточно монет. Требуется: " + -coinsDelta + ", доступно: " + user.getInt("coins"),
        );
      }

      user.set("coins", balance);
      user.set("circle_count", user.getInt("circle_count") + circleDelta);
      user.set("face_replace_count", user.getInt("face_replace_count") + faceDelta);
      txDao.saveRecord(user);

      const operations = txDao.findCollectionByNameOrId("user_operations");
      const operation = new Record(operations);
      operation.set("operation_key", operationKey);
      operation.set("user", userId);
      operation.set("job_id", jobId);
      operation.set("kind", kind);
      operation.set("coins_delta", coinsDelta);
      operation.set("circle_delta", circleDelta);
      operation.set("face_delta", faceDelta);
      operation.set("balance_after", balance);
      txDao.saveRecord(operation);

      result = { applied: true, balance: balance };
    });

    return c.json(200, result);
  },
  $apis.requireAdminAuth(),
);
