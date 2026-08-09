import test from "node:test";
import assert from "node:assert/strict";
import { encodeQRModules, qrToSvg } from "./qrcode.mjs";

test("encodeQRModules builds a square matrix for a tunnel URL", () => {
  const modules = encodeQRModules("https://bright-sun.trycloudflare.com");
  assert.ok(Array.isArray(modules));
  assert.ok(modules.length >= 21);
  assert.equal(modules.length, modules[0].length);
  // Finder pattern top-left dark corner
  assert.equal(modules[0][0], true);
});

test("qrToSvg returns embeddable SVG markup", () => {
  const svg = qrToSvg("https://example.trycloudflare.com", { size: 160 });
  assert.match(svg, /^<svg /);
  assert.match(svg, /viewBox=/);
  assert.match(svg, /<path /);
  assert.match(svg, /role="img"/);
});

test("encodeQRModules rejects empty payload", () => {
  assert.throws(() => encodeQRModules(""), /empty/);
});

// --- Round-trip decoding -----------------------------------------------------
// A matrix that merely looks square proves nothing: the old encoder packed a
// 55-byte URL into version 1 and silently truncated it, so the QR rendered and
// scanned to a cut-off URL. These tests re-read the payload out of the matrix
// the way a scanner does, which is the only assertion that catches that class of
// bug. ECC is not corrected here; only placement, masking, interleaving, and the
// format/version bits are verified.

// Spec tables, kept independent of the implementation on purpose.
const SPEC_ECC_PER_BLOCK_M = [null, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26];
const SPEC_BLOCKS_M = [null, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5];
const SPEC_TOTAL_CODEWORDS = [null, 26, 44, 70, 100, 134, 172, 196, 242, 292, 346];

function alignmentPositions(version) {
  if (version === 1) return [];
  const numAlign = Math.floor(version / 7) + 2;
  const step = Math.ceil((version * 4 + 4) / (numAlign * 2 - 2)) * 2;
  const result = [6];
  for (let pos = version * 4 + 10; result.length < numAlign; pos -= step) {
    result.splice(1, 0, pos);
  }
  return result;
}

/** Rebuild the function-module map for a version, independent of the encoder. */
function functionMap(version) {
  const size = version * 4 + 17;
  const fn = Array.from({ length: size }, () => Array(size).fill(false));
  const mark = (x, y) => {
    if (x >= 0 && y >= 0 && x < size && y < size) fn[y][x] = true;
  };
  for (const [cx, cy] of [[3, 3], [size - 4, 3], [3, size - 4]]) {
    for (let dy = -4; dy <= 4; dy++) for (let dx = -4; dx <= 4; dx++) mark(cx + dx, cy + dy);
  }
  for (let i = 0; i < size; i++) {
    mark(6, i);
    mark(i, 6);
  }
  const align = alignmentPositions(version);
  for (let i = 0; i < align.length; i++) {
    for (let j = 0; j < align.length; j++) {
      if ((i === 0 && j === 0) || (i === 0 && j === align.length - 1)
        || (i === align.length - 1 && j === 0)) continue;
      for (let dy = -2; dy <= 2; dy++) for (let dx = -2; dx <= 2; dx++) mark(align[j] + dx, align[i] + dy);
    }
  }
  for (let i = 0; i <= 8; i++) {
    mark(8, i);
    mark(i, 8);
  }
  for (let i = 0; i < 8; i++) {
    mark(size - 1 - i, 8);
    mark(8, size - 1 - i);
  }
  if (version >= 7) {
    for (let i = 0; i < 18; i++) {
      const a = size - 11 + (i % 3);
      const b = Math.floor(i / 3);
      mark(a, b);
      mark(b, a);
    }
  }
  return fn;
}

function maskBit(mask, x, y) {
  switch (mask) {
    case 0: return (x + y) % 2 === 0;
    case 1: return y % 2 === 0;
    case 2: return x % 3 === 0;
    case 3: return (x + y) % 3 === 0;
    case 4: return (Math.floor(x / 3) + Math.floor(y / 2)) % 2 === 0;
    case 5: return ((x * y) % 2) + ((x * y) % 3) === 0;
    case 6: return (((x * y) % 2) + ((x * y) % 3)) % 2 === 0;
    case 7: return (((x + y) % 2) + ((x * y) % 3)) % 2 === 0;
    default: throw new Error(`bad mask ${mask}`);
  }
}

/** Read the mask index back out of the format bits, as a scanner would. */
function readMask(modules) {
  let raw = 0;
  for (let i = 0; i <= 5; i++) if (modules[i][8]) raw |= 1 << i;
  if (modules[7][8]) raw |= 1 << 6;
  if (modules[8][8]) raw |= 1 << 7;
  if (modules[8][7]) raw |= 1 << 8;
  for (let i = 9; i < 15; i++) if (modules[8][14 - i]) raw |= 1 << i;
  const bits = raw ^ 0x5412;
  const data = bits >>> 10;
  assert.equal(data >>> 3, 0b00, "format bits must declare ECC level M");
  return data & 0b111;
}

/** Undo mask + zig-zag placement and return the interleaved codewords. */
function readCodewords(modules, version) {
  const size = modules.length;
  const fn = functionMap(version);
  const mask = readMask(modules);
  const bits = [];
  for (let right = size - 1; right >= 1; right -= 2) {
    if (right === 6) right = 5;
    for (let vert = 0; vert < size; vert++) {
      for (let j = 0; j < 2; j++) {
        const x = right - j;
        const upward = ((right + 1) & 2) === 0;
        const y = upward ? size - 1 - vert : vert;
        if (fn[y][x]) continue;
        bits.push((modules[y][x] !== maskBit(mask, x, y)) ? 1 : 0);
      }
    }
  }
  const words = [];
  for (let i = 0; i + 8 <= bits.length; i += 8) {
    let b = 0;
    for (let j = 0; j < 8; j++) b = (b << 1) | bits[i + j];
    words.push(b);
  }
  return words;
}

/** Reverse the block interleave and return the concatenated data codewords. */
function deinterleaveData(words, version) {
  const numBlocks = SPEC_BLOCKS_M[version];
  const eccLen = SPEC_ECC_PER_BLOCK_M[version];
  const total = SPEC_TOTAL_CODEWORDS[version];
  const shortBlockLen = Math.floor(total / numBlocks);
  const numShortBlocks = numBlocks - (total % numBlocks);
  const shortDataLen = shortBlockLen - eccLen;

  const blocks = Array.from({ length: numBlocks }, () => []);
  let k = 0;
  for (let i = 0; i < shortDataLen; i++) {
    for (let j = 0; j < numBlocks; j++) blocks[j].push(words[k++]);
  }
  // Long blocks carry one extra data codeword, emitted after the short ones.
  for (let j = numShortBlocks; j < numBlocks; j++) blocks[j].push(words[k++]);
  return blocks.flat();
}

/** Parse a byte-mode segment out of the data codewords. */
function readBytePayload(data, version) {
  const bits = [];
  for (const b of data) for (let i = 7; i >= 0; i--) bits.push((b >>> i) & 1);
  let p = 0;
  const take = (n) => {
    let v = 0;
    for (let i = 0; i < n; i++) v = (v << 1) | bits[p++];
    return v;
  };
  assert.equal(take(4), 0b0100, "payload must use byte mode");
  const length = take(version <= 9 ? 8 : 16);
  const bytes = [];
  for (let i = 0; i < length; i++) bytes.push(take(8));
  return new TextDecoder().decode(new Uint8Array(bytes));
}

function roundTrip(url) {
  const modules = encodeQRModules(url);
  const version = (modules.length - 17) / 4;
  const words = readCodewords(modules, version);
  return { version, text: readBytePayload(deinterleaveData(words, version), version) };
}

test("QR payload survives a round trip at every version it selects", () => {
  const cases = [
    ["https://a-b-c.trycloudflare.com", 3],
    ["https://bright-sun.trycloudflare.com", 3],
    // 45 and 55 bytes previously packed into version 1 and decoded truncated.
    ["https://gentle-river-forest.trycloudflare.com", 4],
    ["https://brave-lion-quiet-harbor-drift.trycloudflare.com", 4],
    [`https://${"x".repeat(60)}.trycloudflare.com`, 6],
  ];
  for (const [url, wantVersion] of cases) {
    const { version, text } = roundTrip(url);
    assert.equal(version, wantVersion, `wrong version for ${url.length} bytes`);
    assert.equal(text, url, `payload did not survive for ${url.length} bytes`);
  }
});

test("encodeQRModules refuses payloads it cannot hold instead of truncating", () => {
  assert.throws(() => encodeQRModules(`https://${"x".repeat(240)}.example.com`), /exceeds|too long/);
});

/** Read the 18-bit version block back, the way a scanner does for version 7+. */
function readVersionBits(modules) {
  const size = modules.length;
  let raw = 0;
  for (let i = 0; i < 18; i++) {
    if (modules[Math.floor(i / 3)][size - 11 + (i % 3)]) raw |= 1 << i;
  }
  return raw >>> 12;
}

test("versions 7 and up reserve and encode the version information block", () => {
  // getNumRawDataModules subtracts 36 modules for these two blocks, so leaving
  // them undrawn let codewords overwrite them and made every version 7+ symbol
  // undecodable. Round-tripping alone cannot catch it because the reader skips
  // that region, so the encoded value itself is asserted here.
  // Byte lengths chosen to sit inside each version's ECC-M byte-mode capacity:
  // v7 holds 122, v8 152, v9 180, v10 213 (capacity minus mode and length bits).
  const cases = [[120, 7], [150, 8], [178, 9], [210, 10]];
  for (const [bytes, wantVersion] of cases) {
    const prefix = "https://";
    const suffix = ".trycloudflare.com";
    const url = prefix + "y".repeat(bytes - prefix.length - suffix.length) + suffix;
    assert.equal(new TextEncoder().encode(url).length, bytes, "test built the wrong payload size");
    const { version, text } = roundTrip(url);
    assert.equal(version, wantVersion, `${url.length} bytes picked version ${version}`);
    assert.equal(text, url, `payload did not survive at version ${wantVersion}`);
    assert.equal(
      readVersionBits(encodeQRModules(url)),
      wantVersion,
      `version block does not declare version ${wantVersion}`,
    );
  }
});

test("mask is selected by penalty rather than hard-coded to zero", () => {
  // Not every payload must avoid mask 0, but a fixed-mask encoder returns 0 for
  // all of them. Distinct payloads should therefore not all report mask 0.
  const masks = new Set();
  for (let i = 0; i < 12; i++) {
    masks.add(readMask(encodeQRModules(`https://sample-${i}-tunnel.trycloudflare.com`)));
  }
  assert.ok(masks.size > 1, `expected varied masks, always got ${[...masks]}`);
});
