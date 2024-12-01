/// <reference path="../pb_data/types.d.ts" />
migrate(
  (db) => {
    const snapshot = [
      {
        id: "2dtkk2h5xo817br",
        created: "2024-11-29 11:45:22.881Z",
        updated: "2024-11-29 11:45:22.881Z",
        name: "circle_jobs",
        type: "base",
        system: false,
        schema: [
          {
            system: false,
            id: "uzfarnhf",
            name: "owner",
            type: "relation",
            required: false,
            presentable: false,
            unique: false,
            options: {
              collectionId: "ojssopdqy5r541p",
              cascadeDelete: false,
              minSelect: null,
              maxSelect: 1,
              displayFields: null,
            },
          },
          {
            system: false,
            id: "gvtrqemw",
            name: "input_media",
            type: "file",
            required: false,
            presentable: false,
            unique: false,
            options: {
              mimeTypes: [],
              thumbs: [],
              maxSelect: 1,
              maxSize: 524288000,
              protected: false,
            },
          },
          {
            system: false,
            id: "reycy6gk",
            name: "output_media",
            type: "file",
            required: false,
            presentable: false,
            unique: false,
            options: {
              mimeTypes: [],
              thumbs: [],
              maxSelect: 1,
              maxSize: 524288000,
              protected: false,
            },
          },
          {
            system: false,
            id: "zi4rfipy",
            name: "status",
            type: "text",
            required: false,
            presentable: false,
            unique: false,
            options: {
              min: null,
              max: null,
              pattern: "",
            },
          },
        ],
        indexes: [],
        listRule: null,
        viewRule: null,
        createRule: null,
        updateRule: null,
        deleteRule: null,
        options: {},
      },
      {
        id: "9r47cxfzaoclhq6",
        created: "2024-11-29 11:45:22.882Z",
        updated: "2024-11-29 11:45:22.882Z",
        name: "face_jobs",
        type: "base",
        system: false,
        schema: [
          {
            system: false,
            id: "qp8ofgdk",
            name: "owner",
            type: "relation",
            required: false,
            presentable: false,
            unique: false,
            options: {
              collectionId: "ojssopdqy5r541p",
              cascadeDelete: false,
              minSelect: null,
              maxSelect: 1,
              displayFields: null,
            },
          },
          {
            system: false,
            id: "vzonmr2d",
            name: "input_face",
            type: "file",
            required: false,
            presentable: false,
            unique: false,
            options: {
              mimeTypes: [],
              thumbs: [],
              maxSelect: 1,
              maxSize: 524288000,
              protected: false,
            },
          },
          {
            system: false,
            id: "zr1yy9v0",
            name: "input_media",
            type: "file",
            required: false,
            presentable: false,
            unique: false,
            options: {
              mimeTypes: [],
              thumbs: [],
              maxSelect: 1,
              maxSize: 524288000,
              protected: false,
            },
          },
          {
            system: false,
            id: "oavl1xr2",
            name: "media_transformed",
            type: "file",
            required: false,
            presentable: false,
            unique: false,
            options: {
              mimeTypes: [],
              thumbs: [],
              maxSelect: 1,
              maxSize: 524288000,
              protected: false,
            },
          },
          {
            system: false,
            id: "vjpc4htl",
            name: "output_media",
            type: "file",
            required: false,
            presentable: false,
            unique: false,
            options: {
              mimeTypes: [],
              thumbs: [],
              maxSelect: 1,
              maxSize: 524288000,
              protected: false,
            },
          },
          {
            system: false,
            id: "db4eyjzd",
            name: "status",
            type: "text",
            required: false,
            presentable: false,
            unique: false,
            options: {
              min: null,
              max: null,
              pattern: "",
            },
          },
        ],
        indexes: [],
        listRule: null,
        viewRule: null,
        createRule: null,
        updateRule: null,
        deleteRule: null,
        options: {},
      },
      {
        id: "ojssopdqy5r541p",
        created: "2024-11-29 11:45:22.882Z",
        updated: "2024-11-29 11:45:22.882Z",
        name: "users",
        type: "base",
        system: false,
        schema: [
          {
            system: false,
            id: "vqlz3wsv",
            name: "tgid",
            type: "number",
            required: false,
            presentable: false,
            unique: false,
            options: {
              min: null,
              max: null,
              noDecimal: false,
            },
          },
          {
            system: false,
            id: "tekw5iqf",
            name: "username",
            type: "text",
            required: false,
            presentable: false,
            unique: false,
            options: {
              min: null,
              max: null,
              pattern: "",
            },
          },
          {
            system: false,
            id: "yinpouxs",
            name: "circle_count",
            type: "number",
            required: false,
            presentable: false,
            unique: false,
            options: {
              min: null,
              max: null,
              noDecimal: false,
            },
          },
          {
            system: false,
            id: "dths5bgp",
            name: "face_replace_count",
            type: "number",
            required: false,
            presentable: false,
            unique: false,
            options: {
              min: null,
              max: null,
              noDecimal: false,
            },
          },
          {
            system: false,
            id: "bb3yst5x",
            name: "coins",
            type: "number",
            required: false,
            presentable: false,
            unique: false,
            options: {
              min: null,
              max: null,
              noDecimal: false,
            },
          },
        ],
        indexes: [],
        listRule: null,
        viewRule: null,
        createRule: null,
        updateRule: null,
        deleteRule: null,
        options: {},
      },
    ];

    const collections = snapshot.map((item) => new Collection(item));

    return Dao(db).importCollections(collections, true, null);
  },
  (db) => {
    return null;
  },
);
