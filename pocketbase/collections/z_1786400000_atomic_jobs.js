/// <reference path="../pb_data/types.d.ts" />
migrate(
  (db) => {
    const dao = new Dao(db);
    const jobCollections = ["circle_jobs", "face_jobs"];

    for (const collectionName of jobCollections) {
      const collection = dao.findCollectionByNameOrId(collectionName);
      collection.schema.addField(
        new SchemaField({
          id: "claimedby01",
          name: "claimed_by",
          type: "text",
          required: false,
          options: { min: null, max: 100, pattern: "" },
        }),
      );
      collection.schema.addField(
        new SchemaField({
          id: "leaseuntil1",
          name: "lease_until",
          type: "date",
          required: false,
          options: { min: "", max: "" },
        }),
      );
      collection.schema.addField(
        new SchemaField({
          id: "attempts001",
          name: "attempts",
          type: "number",
          required: false,
          options: { min: 0, max: 3, noDecimal: true },
        }),
      );
      dao.saveCollection(collection);
    }

    const operations = new Collection({
      id: "userops00000000",
      name: "user_operations",
      type: "base",
      system: false,
      schema: [
        {
          id: "operationkey01",
          name: "operation_key",
          type: "text",
          required: true,
          options: { min: 1, max: 100, pattern: "^[a-zA-Z0-9:_-]+$" },
        },
        {
          id: "userrelation1",
          name: "user",
          type: "relation",
          required: true,
          options: {
            collectionId: "ojssopdqy5r541p",
            cascadeDelete: false,
            minSelect: 1,
            maxSelect: 1,
            displayFields: null,
          },
        },
        {
          id: "jobidfield01",
          name: "job_id",
          type: "text",
          required: true,
          options: { min: 1, max: 30, pattern: "^[a-zA-Z0-9_-]+$" },
        },
        {
          id: "kindfield001",
          name: "kind",
          type: "text",
          required: true,
          options: { min: 1, max: 30, pattern: "^[a-z_]+$" },
        },
        {
          id: "coinsdelta01",
          name: "coins_delta",
          type: "number",
          required: false,
          options: { min: null, max: null, noDecimal: true },
        },
        {
          id: "circledelta1",
          name: "circle_delta",
          type: "number",
          required: false,
          options: { min: 0, max: 1, noDecimal: true },
        },
        {
          id: "facedelta001",
          name: "face_delta",
          type: "number",
          required: false,
          options: { min: 0, max: 1, noDecimal: true },
        },
        {
          id: "balanceafter",
          name: "balance_after",
          type: "number",
          required: true,
          options: { min: 0, max: null, noDecimal: true },
        },
      ],
      indexes: [
        "CREATE UNIQUE INDEX idx_user_operations_key ON user_operations (operation_key)",
        "CREATE INDEX idx_user_operations_user ON user_operations (user)",
      ],
      listRule: null,
      viewRule: null,
      createRule: null,
      updateRule: null,
      deleteRule: null,
      options: {},
    });
    dao.saveCollection(operations);
  },
  (db) => {
    const dao = new Dao(db);
    try {
      dao.deleteCollection(dao.findCollectionByNameOrId("user_operations"));
    } catch (_) {
      // The collection may already be absent after a partial rollback.
    }

    for (const collectionName of ["circle_jobs", "face_jobs"]) {
      const collection = dao.findCollectionByNameOrId(collectionName);
      collection.schema.removeField("claimedby01");
      collection.schema.removeField("leaseuntil1");
      collection.schema.removeField("attempts001");
      dao.saveCollection(collection);
    }
  },
);
