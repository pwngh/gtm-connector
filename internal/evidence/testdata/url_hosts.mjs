// Records the host the URL Standard's parser gives for generated hosts with xn-- labels, for
// TestHostsMatchTheURLParser. Run it with Node from the repository root:
//
//	node internal/evidence/testdata/url_hosts.mjs
//
// It writes internal/evidence/testdata/url_hosts.jsonl.gz: a first line naming Node and its URL parser, then
// one [host, hostname] per input, where hostname is what new URL("https://" + host + "/") gives, or null
// when the parser refuses the URL. The inputs come from a seeded generator, so a run gives the same inputs.
import { writeFileSync } from "node:fs";
import { gzipSync } from "node:zlib";

let seed = 20261121;

// random is mulberry32: a number from 0 up to 1.
function random() {
  seed = (seed + 0x6d2b79f5) | 0;
  let t = seed;
  t = Math.imul(t ^ (t >>> 15), t | 1);
  t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
  return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
}
const int = (n) => Math.floor(random() * n);
const pick = (list) => list[int(list.length)];

// punycode encodes code points as RFC 3492 does, whatever they are, so it can write labels a parser refuses.
function punycode(points) {
  const base = 36, tmin = 1, tmax = 26, skew = 38, damp = 700;
  const digit = (d) => String.fromCharCode(d < 26 ? 97 + d : 22 + d);
  const adapt = (delta, count, first) => {
    delta = first ? Math.floor(delta / damp) : Math.floor(delta / 2);
    delta += Math.floor(delta / count);
    let k = 0;
    for (; delta > ((base - tmin) * tmax) / 2; k += base) delta = Math.floor(delta / (base - tmin));
    return k + Math.floor(((base - tmin + 1) * delta) / (delta + skew));
  };
  let out = points.filter((c) => c < 128).map((c) => String.fromCodePoint(c)).join("");
  const basic = out.length;
  if (basic > 0) out += "-";
  let n = 128, delta = 0, bias = 72, handled = basic;
  while (handled < points.length) {
    const m = Math.min(...points.filter((c) => c >= n));
    delta += (m - n) * (handled + 1);
    n = m;
    for (const c of points) {
      if (c < n) delta++;
      if (c !== n) continue;
      let q = delta;
      for (let k = base; ; k += base) {
        const t = k <= bias ? tmin : k >= bias + tmax ? tmax : k - bias;
        if (q < t) break;
        out += digit(t + ((q - t) % (base - t)));
        q = Math.floor((q - t) / (base - t));
      }
      out += digit(q);
      bias = adapt(delta, handled + 1, handled === basic);
      delta = 0;
      handled++;
    }
    delta++;
    n++;
  }
  return out;
}

const codes = (s) => [...s].map((c) => c.codePointAt(0));

// Pools of characters, by script and by the rule they meet.
const scripts = {
  latin: "éüöäßçñøåłőşğıœ",
  greek: "αβγδεζηθλμνξπρσςωάέ",
  cyrillic: "абвгдежзиклмнопрстуфя",
  han: "中国例字日本語文",
  hangul: "한국어글",
  kana: "あいうえおアイウエオー",
  thai: "กขคงจไเ",
  devanagari: "कखगषभरतािीे्",
  arabic: "ابتثجحخدملنه",
  hebrew: "אבגדהולםש",
  emoji: "☃💩",
  unicode16: "\u{1E5D0}\u{1E5D1}\u{10D50}",
};
const extras = {
  ascii: "abcdefghijklmnopqrstuvwxyz0123456789-",
  upper: "ÜÉΩЖÀ",
  arabicDigits: "٠١٢٣",
  persianDigits: "۰۱۲۳",
  marks: "ְً́̈",
  joiners: "‌‍",
  odd: "­� 。Ａ_ \u0080͸",
};
const all = { ...scripts, ...extras };

function draw(pool, count) {
  const chars = codes(pool);
  return Array.from({ length: count }, () => pick(chars));
}

// label is an xn-- label: mostly one script, sometimes with digits, marks, joiners or another script, and
// sometimes changed after it is encoded.
function label() {
  const roll = random();
  let points;
  if (roll < 0.1) {
    return "xn--" + draw(extras.ascii, 1 + int(8)).map((c) => String.fromCodePoint(c)).join("");
  } else if (roll < 0.5) {
    points = draw(pick(Object.values(scripts)), 1 + int(6));
  } else if (roll < 0.75) {
    points = draw(pick(Object.values(scripts)), 1 + int(5));
    const extra = draw(pick(Object.values(extras)), 1 + int(2));
    for (const c of extra) points.splice(int(points.length + 1), 0, c);
  } else if (roll < 0.9) {
    points = draw(pick(Object.values(all)) + pick(Object.values(all)), 1 + int(6));
  } else {
    points = draw(pick([scripts.hebrew, scripts.arabic]) + pick(Object.values(extras)), 1 + int(6));
  }
  let encoded = "xn--" + punycode(points);
  if (random() < 0.2) {
    const chars = [...encoded];
    const at = 4 + int(chars.length - 3);
    switch (int(5)) {
      case 0:
        chars[Math.min(at, chars.length - 1)] = pick([..."abcxyz019-"]);
        break;
      case 1:
        chars.splice(Math.min(at, chars.length - 1), 1);
        break;
      case 2:
        chars.splice(at, 0, pick([..."akz09-"]));
        break;
      case 3:
        chars.push("-");
        break;
      default:
        chars[Math.min(at, chars.length - 1)] = chars[Math.min(at, chars.length - 1)].toUpperCase();
    }
    encoded = chars.join("");
  }
  if (random() < 0.05) encoded = "XN" + encoded.slice(2);
  return encoded;
}

const plain = ["example", "files", "a1", "1", "a-", "-a", "co", "b2b", "x", "123", "a--b"];

function host() {
  const labels = [];
  const count = 1 + int(3);
  const at = int(count);
  for (let i = 0; i < count; i++) labels.push(i === at || random() < 0.2 ? label() : pick(plain));
  let name = labels.join(".");
  if (random() < 0.1) name = "www." + name;
  if (random() < 0.05) name += ".";
  return name;
}

const seen = new Set();
const lines = [JSON.stringify({ node: process.version, ada: process.versions.ada, unicode: process.versions.unicode })];
while (seen.size < 20000) {
  const name = host();
  if (name.length > 75 || seen.has(name)) continue;
  seen.add(name);
  let hostname = null;
  try {
    hostname = new URL("https://" + name + "/").hostname;
  } catch {
    // The parser refuses the URL.
  }
  lines.push(JSON.stringify([name, hostname]));
}
writeFileSync(new URL("url_hosts.jsonl.gz", import.meta.url), gzipSync(lines.join("\n") + "\n", { level: 9 }));
