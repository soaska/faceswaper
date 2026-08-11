/// <reference path="../pb_data/types.d.ts" />
migrate(
  (db) => {
    const dao = new Dao(db);
    for (const collectionName of ["circle_jobs", "face_jobs"]) {
      const collection = dao.findCollectionByNameOrId(collectionName);
      collection.schema.addField(
        new SchemaField({
          id: "requestkey01",
          name: "request_key",
          type: "text",
          required: false,
          options: { min: null, max: 100, pattern: "^[a-zA-Z0-9:_-]*$" },
        }),
      );
      collection.indexes = collection.indexes || [];
      collection.indexes.push(
        "CREATE UNIQUE INDEX idx_" +
          collectionName +
          "_request_key ON " +
          collectionName +
          " (request_key) WHERE request_key != ''",
      );
      dao.saveCollection(collection);
    }
  },
  (db) => {
    const dao = new Dao(db);
    for (const collectionName of ["circle_jobs", "face_jobs"]) {
      const collection = dao.findCollectionByNameOrId(collectionName);
      collection.schema.removeField("requestkey01");
      collection.indexes = (collection.indexes || []).filter(
        (index) => index.indexOf("idx_" + collectionName + "_request_key") === -1,
      );
      dao.saveCollection(collection);
    }
  },
);
