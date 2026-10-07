import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { compileFunction } from "node:vm";
import ts from "typescript";
import * as vue from "vue";

// 使用真实 Vue 响应式验证现有 setup 逻辑, 仅替代 UI 和 API 边界.
// 定向验证不依赖浏览器、后端或额外测试框架.
function setupComponent(name, props, exposed, overrides = {}) {
  const source = readFileSync(
    new URL(`../src/components/keys/${name}.vue`, import.meta.url),
    "utf8"
  );
  const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1];
  const ast = ts.createSourceFile(`${name}.ts`, script, ts.ScriptTarget.Latest, true);
  const bindings = {
    defineProps: () => props,
    withDefaults: value => value,
    defineEmits: () => () => {},
  };
  for (const statement of ast.statements) {
    if (!ts.isImportDeclaration(statement)) {
      continue;
    }
    const clause = statement.importClause;
    if (clause?.isTypeOnly) {
      continue;
    }
    const names = clause?.namedBindings;
    if (names && ts.isNamedImports(names)) {
      for (const element of names.elements) {
        if (!element.isTypeOnly) {
          bindings[element.name.text] =
            statement.moduleSpecifier.text === "vue" ? vue[element.name.text] : () => {};
        }
      }
    } else if (clause?.name) {
      bindings[clause.name.text] = () => {};
    }
  }
  Object.assign(bindings, {
    useI18n: () => ({ t: key => key }),
    useMessage: () => ({ error() {}, warning() {} }),
    useDialog: () => ({}),
    appState: vue.reactive({ groupDataRefreshTrigger: 0 }),
    settingsApi: { getChannelTypes: async () => ["openai", "other"] },
    keysApi: { getGroupConfigOptions: async () => [] },
    ...overrides,
  });
  const output = ts.transpileModule(script, {
    compilerOptions: { target: ts.ScriptTarget.ESNext, module: ts.ModuleKind.ESNext },
    transformers: {
      before: [
        () => node =>
          ts.factory.updateSourceFile(
            node,
            node.statements.filter(statement => !ts.isImportDeclaration(statement))
          ),
      ],
    },
  }).outputText;
  const execute = compileFunction(
    `${output.replace(/export \{\};?\s*$/, "")}\nreturn { ${exposed.join(", ")} };`,
    Object.keys(bindings)
  );
  return execute(...Object.values(bindings));
}

const formExposed = [
  "formData",
  "rules",
  "formRef",
  "removeConfigItem",
  "handleConfigKeyChange",
  "configOptions",
  "channelTypeOptions",
  "userModifiedFields",
  "handleSubmit",
];

async function createForm(group = null, overrides = {}) {
  const props = vue.reactive({ show: false, group });
  const form = setupComponent("GroupFormModal", props, formExposed, overrides);
  props.show = true;
  await vue.nextTick();
  return form;
}

test("other defaults to zero retries without writing translated placeholders into fields", async () => {
  const form = await createForm();
  form.formData.channel_type = "other";
  assert.deepEqual(form.formData.configItems, [{ key: "max_retries", value: 0 }]);
  assert.equal(form.formData.upstreams[0].url, "");
  assert.equal(form.formData.test_model, "gpt-4.1-nano");
  assert.equal(form.rules.value.test_model[0].required, false);
  assert.ok(form.channelTypeOptions.value.some(option => option.value === "other"));
});

test("custom upstream URLs survive other transitions and ordinary defaults still update", async () => {
  const form = await createForm();
  form.formData.channel_type = "gemini";
  assert.equal(form.formData.test_model, "gemini-2.0-flash-lite");
  assert.equal(form.formData.upstreams[0].url, "https://generativelanguage.googleapis.com");
  form.formData.upstreams[0].url = "https://custom.example.test";
  form.userModifiedFields.value.upstream = true;
  form.formData.channel_type = "other";
  assert.equal(form.formData.upstreams[0].url, "https://custom.example.test");
  form.formData.channel_type = "anthropic";
  assert.equal(form.formData.upstreams[0].url, "https://custom.example.test");
});

test("switching into and out of other preserves explicit fields and configuration", async () => {
  const form = await createForm();
  form.formData.configItems.push({ key: "max_retries", value: 7 });
  form.formData.test_model = "custom-model";
  form.formData.validation_endpoint = "/custom/test";
  form.formData.model_redirect_rules = '{"original":"target"}';
  form.formData.model_redirect_strict = true;
  form.formData.header_rules.push({ key: "X-Test", value: "kept", action: "set" });
  form.formData.param_overrides = '{"temperature":0.5}';
  form.formData.channel_type = "other";
  form.formData.channel_type = "gemini";
  assert.deepEqual(form.formData.configItems, [{ key: "max_retries", value: 7 }]);
  assert.equal(form.formData.test_model, "custom-model");
  assert.equal(form.formData.validation_endpoint, "/custom/test");
  assert.equal(form.formData.model_redirect_rules, '{"original":"target"}');
  assert.equal(form.formData.model_redirect_strict, true);
  assert.equal(form.formData.header_rules[0].value, "kept");
  assert.equal(form.formData.param_overrides, '{"temperature":0.5}');
  assert.equal(form.rules.value.test_model[0].required, true);
});

test("deleting max_retries submits no override, leaving the default to the backend", async () => {
  let submitted;
  const form = await createForm(null, {
    keysApi: {
      getGroupConfigOptions: async () => [],
      createGroup: async payload => {
        submitted = payload;
        return { ...payload, id: 1 };
      },
    },
  });
  form.formData.channel_type = "other";
  form.formData.test_model = "";
  form.removeConfigItem(0);
  await vue.nextTick();
  form.formRef.value = { validate: async () => {} };
  await form.handleSubmit();
  assert.deepEqual(submitted.config, {});
  assert.equal(submitted.test_model, "");
});

test("loading other retains explicit retries and fills only an absent retry item", async () => {
  for (const retries of [undefined, 0, 4]) {
    const group = {
      id: 1,
      channel_type: "other",
      config: retries === undefined ? {} : { max_retries: retries },
      test_model: "saved-model",
      validation_endpoint: "/saved/test",
      upstreams: [{ url: "https://example.test", weight: 1 }],
    };
    const form = await createForm(group);
    assert.equal(form.formData.configItems[0].value, retries ?? 0);
    assert.equal(form.formData.test_model, "saved-model");
    assert.equal(form.formData.validation_endpoint, "/saved/test");
    assert.equal(form.formData.upstreams[0].url, "https://example.test");
    assert.equal(form.rules.value.test_model[0].required, false);
  }
});

test("selecting max_retries in advanced configuration uses the channel-specific default", async () => {
  const form = await createForm();
  form.configOptions.value = [{ key: "max_retries", default_value: 3 }];
  form.formData.channel_type = "other";
  form.handleConfigKeyChange(0, "max_retries");
  assert.equal(form.formData.configItems[0].value, 0);
  form.formData.channel_type = "openai";
  assert.equal(form.formData.configItems[0].value, 0);
  form.handleConfigKeyChange(0, "max_retries");
  assert.equal(form.formData.configItems[0].value, 3);
});

test("other blocks single and batch testing, while keeping manual restore menu entries", async () => {
  let calls = 0;
  const props = vue.reactive({ selectedGroup: null });
  const table = setupComponent("KeyTable", props, ["testKey", "validateKeys", "moreOptions"], {
    keysApi: {
      getGroupKeys: async () => ({ items: [], pagination: { total_items: 0, total_pages: 0 } }),
      testKeys: async () => calls++,
      validateGroupKeys: async () => calls++,
    },
  });
  props.selectedGroup = { id: 1, channel_type: "other" };
  await vue.nextTick();
  // 两个保护分支都在实际测试 API 调用前终止, 避免禁用渠道发出校验请求.
  const originalWindow = globalThis.window;
  globalThis.window = { $message: { info() {} } };
  try {
    await table.testKey({ key_value: "secret" });
    await table.validateKeys("all");
  } finally {
    globalThis.window = originalWindow;
  }
  assert.equal(calls, 0);
  assert.ok(table.moreOptions.value.some(option => option.key === "restoreAll"));
  assert.ok(!table.moreOptions.value.some(option => option.key?.startsWith("validate")));
  props.selectedGroup = { id: 2, channel_type: "openai" };
  assert.ok(table.moreOptions.value.some(option => option.key === "validateAll"));
});

test("aggregate channels and subgroup selection reject other", () => {
  const aggregate = setupComponent(
    "AggregateGroupModal",
    vue.reactive({ show: false, group: null }),
    ["channelTypeOptions", "rules"]
  );
  assert.ok(!aggregate.channelTypeOptions.some(option => option.value === "other"));
  assert.ok(aggregate.rules.channel_type[1].validator(null, "other") instanceof Error);
  const props = vue.reactive({
    show: false,
    aggregateGroup: { id: 1, channel_type: "other" },
    existingSubGroups: [],
    groups: [{ id: 2, channel_type: "other", group_type: "standard" }],
  });
  const subgroup = setupComponent("AddSubGroupModal", props, ["getAvailableOptions", "rules"]);
  assert.deepEqual(subgroup.getAvailableOptions.value, []);
  assert.ok(
    subgroup.rules.sub_groups.validator(null, [{ group_id: 2, weight: 1 }]) instanceof Error
  );
});
