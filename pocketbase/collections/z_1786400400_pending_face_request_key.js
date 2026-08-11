/// <reference path="../pb_data/types.d.ts" />
migrate(
  (db) => {
    const dao = new Dao(db);
    const users = dao.findCollectionByNameOrId("users");
    users.schema.addField(
      new SchemaField({
        id: "pendingreq01",
        name: "pending_face_request_key",
        type: "text",
        required: false,
        options: { min: null, max: 100, pattern: "^telegram:[0-9]+$" },
      }),
    );
    dao.saveCollection(users);
  },
  (db) => {
    const dao = new Dao(db);
    const users = dao.findCollectionByNameOrId("users");
    users.schema.removeField("pendingreq01");
    dao.saveCollection(users);
  },
);
