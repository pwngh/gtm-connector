import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';

// types.ts mirrors the view structs the server writes. This reads the Go
// files and types.ts as text and holds them in step: every json tag has a
// field, every field has a tag, omitempty is optional, and a Go pointer,
// slice or map is nullable.

interface Field {
  name: string;
  optional: boolean;
  nullable: boolean;
}

const goFiles = ['evidence.go', 'session.go', 'matches.go'].map(name =>
  readFileSync(new URL(`../../../internal/transport/http/${name}`, import.meta.url), 'utf8'),
);
const typesSource = readFileSync(new URL('./types.ts', import.meta.url), 'utf8');

const pascal = (name: string) => name.charAt(0).toUpperCase() + name.slice(1);

// Every struct with json tags, by its Go name, with an embedded struct's fields promoted.
function goStructs(): Map<string, Field[]> {
  const own = new Map<string, {fields: Field[]; embeds: string[]}>();
  for (const source of goFiles) {
    for (const match of source.matchAll(/^type (\w+) struct \{\n([\s\S]*?)\n\}/gm)) {
      const [, name = '', body = ''] = match;
      const fields: Field[] = [];
      const embeds: string[] = [];
      for (const raw of body.split('\n')) {
        const line = raw.replace(/\/\/.*$/, '').trim();
        const tagged = line.match(/^\w+\s+(\S+)\s+`json:"([^"]*)"`$/);
        if (tagged) {
          const [, type = '', tag = ''] = tagged;
          const [jsonName = '', ...options] = tag.split(',');
          if (jsonName !== '-') {
            fields.push({name: jsonName, optional: options.includes('omitempty'), nullable: /^(\*|\[\]|map\[)/.test(type)});
          }
        } else if (/^\w+$/.test(line)) {
          embeds.push(line);
        }
      }
      if (fields.length || embeds.length) {
        own.set(name, {fields, embeds});
      }
    }
  }
  const all = new Map<string, Field[]>();
  const resolve = (name: string): Field[] => {
    const struct = own.get(name);
    assert.ok(struct, `the Go struct ${name} was not found`);
    return [...struct.embeds.flatMap(resolve), ...struct.fields];
  };
  for (const name of own.keys()) {
    all.set(name, resolve(name));
  }
  return all;
}

// Every interface in types.ts, with an extended interface's fields included.
function tsInterfaces(): Map<string, Field[]> {
  const own = new Map<string, {fields: Field[]; parents: string[]}>();
  for (const match of typesSource.matchAll(/^export interface (\w+)(?: extends ([\w, ]+))? \{\n([\s\S]*?)\n\}/gm)) {
    const [, name = '', parents = '', body = ''] = match;
    const fields: Field[] = [];
    for (const line of body.split('\n')) {
      const field = line.match(/^ {2}(\w+)(\??): (.+);$/);
      if (field) {
        const [, fieldName = '', optional = '', type = ''] = field;
        fields.push({name: fieldName, optional: optional === '?', nullable: /\bnull\b|\bJson\b/.test(type)});
      }
    }
    own.set(name, {fields, parents: parents ? parents.split(',').map(parent => parent.trim()) : []});
  }
  const all = new Map<string, Field[]>();
  const resolve = (name: string): Field[] => {
    const entry = own.get(name);
    assert.ok(entry, `types.ts has no interface ${name}`);
    return [...entry.parents.flatMap(resolve), ...entry.fields];
  };
  for (const name of own.keys()) {
    all.set(name, resolve(name));
  }
  return all;
}

test('types.ts has a field for every json tag of the server view structs, and no field without one', () => {
  const structs = goStructs();
  const interfaces = tsInterfaces();
  assert.ok(structs.size >= 9, `${structs.size} structs with json tags were found`);
  for (const [goName, tags] of structs) {
    const fields = interfaces.get(pascal(goName));
    assert.ok(fields, `types.ts has no interface ${pascal(goName)} for the Go struct ${goName}`);
    const byName = new Map(fields.map(field => [field.name, field]));
    for (const tag of tags) {
      const field = byName.get(tag.name);
      assert.ok(field, `${pascal(goName)} has no field for the json tag "${tag.name}"`);
      assert.equal(field.optional, tag.optional, `${pascal(goName)}.${tag.name} is optional exactly when its tag says omitempty`);
      if (tag.nullable && !tag.optional) {
        assert.ok(field.nullable, `${pascal(goName)}.${tag.name} can be null, as its Go field can be nil`);
      }
    }
    const tagged = new Set(tags.map(tag => tag.name));
    for (const field of fields) {
      assert.ok(tagged.has(field.name), `${pascal(goName)}.${field.name} has no json tag in ${goName}`);
    }
  }
  // An interface named like a view struct must still have its struct.
  for (const name of interfaces.keys()) {
    if (/View$/.test(name)) {
      assert.ok(
        [...structs.keys()].some(goName => pascal(goName) === name),
        `${name} mirrors no Go struct`,
      );
    }
  }
});
