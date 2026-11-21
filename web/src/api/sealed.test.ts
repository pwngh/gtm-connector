import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {test} from 'node:test';
import {checkSeal} from '../app/deals/card/sealCheck.ts';
import {type Api, ApiError, createApiClient} from './client.ts';
import {sealedText} from './sealed.ts';
import {type SealedCard, sealedCards} from './sealedCards.ts';

type Files = Pick<Api, 'cardBytes' | 'cardText'>;

// A server that answers one card's bytes, digest header and text, each as given.
function serving(card: SealedCard, {bytes = card.bytes, header = card.sha256 as string | null, text = card.text} = {}): Files & {reads: number} {
  const files = {
    reads: 0,
    async cardBytes(id: string) {
      assert.equal(id, card.key);
      files.reads += 1;
      return {bytes, sha256: header};
    },
    async cardText(id: string) {
      assert.equal(id, card.key);
      return text;
    },
  };
  return files;
}
const asked = (card: SealedCard, sha256 = card.sha256) => ({id: card.key, sha256});
const thornfield = sealedCards.find(card => card.file === 'demo' && card.key === 'acc-003') as SealedCard;
const altered = (bytes: Uint8Array<ArrayBuffer>, at = 40) => {
  const copy = new Uint8Array(bytes);
  copy[at] = (copy[at] as number) ^ 1;
  return copy;
};

test('every recorded card is read whole: its bytes match its seal and the answer is its text, and nothing else', async () => {
  assert.equal(sealedCards.length, 450);
  for (const card of sealedCards) {
    const at = `${card.file} ${card.key}`;
    assert.equal(createHash('sha256').update(card.bytes).digest('hex'), card.sha256, `${at}: the recorded digest is the bytes' digest`);
    const result = await sealedText(serving(card), asked(card));
    assert.deepEqual(Object.keys(result).sort(), ['outcome', 'text'], at);
    assert.deepEqual(result, {outcome: 'verified', text: card.text}, at);
  }
});

test('one byte changed, a header or a card digest that differs, or no header, answers failed and nothing else', async () => {
  const other = sealedCards.find(card => card.sha256 !== thornfield.sha256) as SealedCard;
  const cases: [string, Files, {id: string; sha256: string}][] = [
    ['one byte of the bytes changed', serving(thornfield, {bytes: altered(thornfield.bytes)}), asked(thornfield)],
    ['the last byte changed', serving(thornfield, {bytes: altered(thornfield.bytes, thornfield.bytes.length - 1)}), asked(thornfield)],
    ['the header names another digest', serving(thornfield, {header: other.sha256}), asked(thornfield)],
    ['the header is in capitals', serving(thornfield, {header: thornfield.sha256.toUpperCase()}), asked(thornfield)],
    ['no header', serving(thornfield, {header: null}), asked(thornfield)],
    ['the card names another digest', serving(thornfield), asked(thornfield, other.sha256)],
    ['the served text differs from the sealed text', serving(thornfield, {text: `${thornfield.text} `}), asked(thornfield)],
  ];
  for (const [what, files, card] of cases) {
    const result = await sealedText(files, card);
    assert.deepEqual(result, {outcome: 'failed'}, what);
    assert.deepEqual(Object.keys(result), ['outcome'], what);
  }
});

test('bytes that match their seal but hold no text string answer failed', async () => {
  for (const file of ['not json', '{"text": 5}', 'null', '["text"]', '{"evidence": {"text": "raw"}}']) {
    const bytes = new Uint8Array(new TextEncoder().encode(file));
    const digest = createHash('sha256').update(bytes).digest('hex');
    const card = {...thornfield, bytes, sha256: digest, text: 'raw'};
    assert.deepEqual(await sealedText(serving(card), asked(card)), {outcome: 'failed'}, file);
  }
});

test('a refused read is thrown as the server refused it, not answered as failed', async () => {
  const refused = new ApiError({status: 403, kind: 'not_permitted', code: 'not_permitted'});
  await assert.rejects(sealedText({...serving(thornfield), cardBytes: () => Promise.reject(refused)}, asked(thornfield)), refused);
  await assert.rejects(sealedText({...serving(thornfield), cardText: () => Promise.reject(refused)}, asked(thornfield)), refused);
});

test('the client reads the bytes and the header exactly, the text only as UTF-8 text/plain, and a refusal as a refusal', async () => {
  const paths: string[] = [];
  const answers: Response[] = [
    new Response(thornfield.bytes, {headers: {'Content-Type': 'application/octet-stream', 'X-Content-SHA256': thornfield.sha256}}),
    new Response(thornfield.text, {headers: {'Content-Type': 'text/plain; charset=utf-8'}}),
    new Response(thornfield.text, {headers: {'Content-Type': 'text/html'}}),
    new Response(JSON.stringify({error: {code: 'not_permitted', message: 'Refused.'}}), {status: 403, headers: {'Content-Type': 'application/json'}}),
    new Response(new Uint8Array([0xff, 0xfe]), {headers: {'Content-Type': 'text/plain; charset=utf-8'}}),
  ];
  const api = createApiClient(async path => {
    paths.push(path);
    return answers.shift() as Response;
  });
  const read = await api.cardBytes('card/1');
  assert.deepEqual(read.bytes, thornfield.bytes);
  assert.equal(read.sha256, thornfield.sha256);
  assert.equal(await api.cardText('card-1'), thornfield.text);
  await assert.rejects(api.cardText('card-1'), {kind: 'unexpected'});
  await assert.rejects(api.cardBytes('card-1'), {kind: 'not_permitted', code: 'not_permitted'});
  await assert.rejects(api.cardText('card-1'), {kind: 'unexpected'});
  assert.deepEqual(paths, [
    '/api/v1/cards/card%2F1/bytes',
    '/api/v1/cards/card-1/text',
    '/api/v1/cards/card-1/text',
    '/api/v1/cards/card-1/bytes',
    '/api/v1/cards/card-1/text',
  ]);
});

test('a card that failed stays failed for the rest of the session, and a new session checks it again', async () => {
  const store = {};
  const bad = serving(thornfield, {bytes: altered(thornfield.bytes)});
  assert.deepEqual(await checkSeal(store, 1, bad, asked(thornfield)), {outcome: 'failed'});
  const good = serving(thornfield);
  assert.deepEqual(await checkSeal(store, 1, good, asked(thornfield)), {outcome: 'failed'});
  assert.equal(good.reads, 0, 'a remembered failure reads nothing');
  assert.deepEqual(await checkSeal(store, 2, good, asked(thornfield)), {outcome: 'verified', text: thornfield.text});
  assert.equal(good.reads, 1);
  assert.deepEqual(await checkSeal({}, 1, good, asked(thornfield)), {outcome: 'verified', text: thornfield.text}, 'another store keeps its own');
});
