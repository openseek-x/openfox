import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { resolve, dirname, join } from "node:path";
import { parse } from "yaml";
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";

const read = (file) => readFileSync(file, "utf8");
const documents = new Map();
function document(file) {
  if (!documents.has(file)) documents.set(file, parse(read(file)));
  return documents.get(file);
}
function dereference(value, file) {
  if (!value.$ref) return { value, file };
  const [relative, pointer] = value.$ref.split("#");
  const target = relative ? resolve(dirname(file), relative) : file;
  const selected = pointer
    .slice(1)
    .split("/")
    .reduce(
      (obj, part) => obj[part.replaceAll("~1", "/").replaceAll("~0", "~")],
      document(target),
    );
  return dereference(selected, target);
}

const spec = resolve("contracts/openapi.yaml");
const routes = new Set();
let operations = 0;
for (const [path, raw] of Object.entries(document(spec).paths)) {
  const { value: item, file } = dereference(raw, spec);
  for (const method of [
    "get",
    "post",
    "patch",
    "put",
    "delete",
    "options",
    "head",
    "trace",
  ]) {
    if (!item[method]) continue;
    operations++;
    let paths = [path];
    const parameters = [
      ...(item.parameters ?? []),
      ...(item[method].parameters ?? []),
    ];
    const byName = new Map();
    for (const parameter of parameters) {
      const { value, file: origin } = dereference(parameter, file);
      if (value.in !== "path") continue;
      byName.set(value.name, dereference(value.schema, origin).value);
    }
    for (const [name, schema] of byName) {
      if (!schema.enum) continue;
      paths = paths.flatMap((p) =>
        schema.enum.map((choice) => p.replace(`{${name}}`, choice)),
      );
    }
    for (const p of paths) routes.add(`${method.toUpperCase()} ${p}`);
  }
}
execFileSync("go", ["run", "./tests/contracts/routes"], {
  input: JSON.stringify([...routes]),
  stdio: ["pipe", "inherit", "inherit"],
});

const validator = new Ajv2020({ strictTypes: false, allErrors: true });
addFormats(validator);
const validate = validator.compile(
  JSON.parse(read("contracts/schemas/device-event.schema.json")),
);
for (const [name, expected] of [
  ["valid", true],
  ["invalid", false],
]) {
  const event = JSON.parse(read(`contracts/samples/device-event.${name}.json`));
  assert.equal(
    validate(event),
    expected,
    `设备事件 ${name} 样例校验有误 ${JSON.stringify(validate.errors)}`,
  );
}

const scratch = mkdtempSync(join(tmpdir(), "openfox-generated-api-"));
try {
  const generated = join(scratch, "schema.gen.ts");
  execFileSync("pnpm", ["exec", "openapi-typescript", spec, "-o", generated], {
    stdio: "inherit",
  });
  assert.ok(
    read(generated) === read("packages/api/src/schema.gen.ts"),
    "契约类型已过期，请运行 pnpm gen:api",
  );
} finally {
  rmSync(scratch, { recursive: true, force: true });
}
console.log(`${operations} 项 HTTP 操作、设备事件正反例和生成类型均已核对。`);
