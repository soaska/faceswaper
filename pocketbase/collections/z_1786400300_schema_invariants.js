/// <reference path="../pb_data/types.d.ts" />
migrate(
  (db) => {
    const dao = new Dao(db);
    const users = dao.findCollectionByNameOrId("users");

    const tgid = users.schema.getFieldByName("tgid");
    tgid.required = true;
    tgid.options.min = 1;
    tgid.options.noDecimal = true;

    for (const fieldName of ["circle_count", "face_replace_count", "coins"]) {
      const field = users.schema.getFieldByName(fieldName);
      field.options.min = 0;
      field.options.noDecimal = true;
    }
    users.indexes = users.indexes || [];
    users.indexes.push("CREATE UNIQUE INDEX idx_users_tgid ON users (tgid)");
    dao.saveCollection(users);

    for (const collectionName of ["circle_jobs", "face_jobs"]) {
      const collection = dao.findCollectionByNameOrId(collectionName);
      collection.schema.getFieldByName("owner").required = true;
      collection.schema.getFieldByName("input_media").required = true;
      collection.schema.getFieldByName("status").required = true;
      if (collectionName === "face_jobs") {
        collection.schema.getFieldByName("input_face").required = true;
        for (const fieldName of ["duration", "price", "threads"]) {
          const field = collection.schema.getFieldByName(fieldName);
          field.options.min = 0;
          field.options.noDecimal = true;
        }
      }
      collection.indexes = collection.indexes || [];
      collection.indexes.push(
        "CREATE INDEX idx_" +
          collectionName +
          "_queue ON " +
          collectionName +
          " (status, created)",
      );
      collection.indexes.push(
        "CREATE INDEX idx_" +
          collectionName +
          "_owner_status ON " +
          collectionName +
          " (owner, status)",
      );
      dao.saveCollection(collection);
    }
  },
  (db) => {
    const dao = new Dao(db);
    const users = dao.findCollectionByNameOrId("users");
    const tgid = users.schema.getFieldByName("tgid");
    tgid.required = false;
    tgid.options.min = null;
    tgid.options.noDecimal = false;
    for (const fieldName of ["circle_count", "face_replace_count", "coins"]) {
      const field = users.schema.getFieldByName(fieldName);
      field.options.min = null;
      field.options.noDecimal = false;
    }
    users.indexes = (users.indexes || []).filter(
      (index) => index.indexOf("idx_users_tgid") === -1,
    );
    dao.saveCollection(users);

    for (const collectionName of ["circle_jobs", "face_jobs"]) {
      const collection = dao.findCollectionByNameOrId(collectionName);
      collection.schema.getFieldByName("owner").required = false;
      collection.schema.getFieldByName("input_media").required = false;
      collection.schema.getFieldByName("status").required = false;
      if (collectionName === "face_jobs") {
        collection.schema.getFieldByName("input_face").required = false;
        for (const fieldName of ["duration", "price", "threads"]) {
          collection.schema.getFieldByName(fieldName).options.min = null;
        }
      }
      collection.indexes = (collection.indexes || []).filter(
        (index) =>
          index.indexOf("idx_" + collectionName + "_queue") === -1 &&
          index.indexOf("idx_" + collectionName + "_owner_status") === -1,
      );
      dao.saveCollection(collection);
    }
  },
);
