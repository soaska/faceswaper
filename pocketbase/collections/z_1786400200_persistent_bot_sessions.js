/// <reference path="../pb_data/types.d.ts" />
migrate(
  (db) => {
    const dao = new Dao(db);
    const users = dao.findCollectionByNameOrId("users");
    users.schema.addField(
      new SchemaField({
        id: "pendingface1",
        name: "pending_face_file_id",
        type: "text",
        required: false,
        options: { min: null, max: 500, pattern: "" },
      }),
    );
    users.schema.addField(
      new SchemaField({
        id: "pendingdate1",
        name: "pending_face_updated",
        type: "date",
        required: false,
        options: { min: "", max: "" },
      }),
    );
    dao.saveCollection(users);
  },
  (db) => {
    const dao = new Dao(db);
    const users = dao.findCollectionByNameOrId("users");
    users.schema.removeField("pendingface1");
    users.schema.removeField("pendingdate1");
    dao.saveCollection(users);
  },
);
