/**
 * Minimal QR Code generator (byte mode, ECC M) for offline tunnel URLs.
 * No network dependency — suitable for localhost / desktop shell settings.
 *
 * Ported from a compact public-domain style encoder; API is local-only.
 */

// ECC codewords PER BLOCK (ISO/IEC 18004 tables), L, M, Q, H for versions 1..10.
// These are per-block counts because getNumDataCodewords and addEccAndInterleave
// both multiply by NUM_ERROR_CORRECTION_BLOCKS. Storing per-version totals here
// double-counted ECC from version 4 onward and produced negative capacities.
const ECC_CODEWORDS_PER_BLOCK = [
  null,
  [7, 10, 13, 17],
  [10, 16, 22, 28],
  [15, 26, 18, 22],
  [20, 18, 26, 16],
  [26, 24, 18, 22],
  [18, 16, 24, 28],
  [20, 18, 18, 26],
  [24, 22, 22, 26],
  [30, 22, 20, 24],
  [18, 26, 24, 28],
];

const NUM_ERROR_CORRECTION_BLOCKS = [
  null,
  [1, 1, 1, 1],
  [1, 1, 1, 1],
  [1, 1, 2, 2],
  [1, 2, 2, 4],
  [1, 2, 4, 4],
  [2, 4, 4, 4],
  [2, 4, 6, 5],
  [2, 4, 6, 6],
  [2, 5, 8, 8],
  [4, 5, 8, 8],
];

function gfMul(x, y) {
  if (x === 0 || y === 0) return 0;
  let z = 0;
  for (let i = 0; i < 8; i++) {
    if ((y & (1 << i)) !== 0) z ^= x << i;
  }
  for (let i = 14; i >= 8; i--) {
    if ((z & (1 << i)) !== 0) z ^= 0x11d << (i - 8);
  }
  return z;
}

function reedSolomonComputeDivisor(degree) {
  const result = new Array(degree).fill(0);
  result[degree - 1] = 1;
  let root = 1;
  for (let i = 0; i < degree; i++) {
    for (let j = 0; j < result.length; j++) {
      result[j] = gfMul(result[j], root);
      if (j + 1 < result.length) result[j] ^= result[j + 1];
    }
    root = gfMul(root, 2);
  }
  return result;
}

function reedSolomonComputeRemainder(data, divisor) {
  const result = new Array(divisor.length).fill(0);
  for (const b of data) {
    const factor = b ^ result[0];
    result.shift();
    result.push(0);
    for (let i = 0; i < result.length; i++) {
      result[i] ^= gfMul(divisor[i], factor);
    }
  }
  return result;
}

function getNumRawDataModules(version) {
  let result = (16 * version + 128) * version + 64;
  if (version >= 2) {
    const numAlign = Math.floor(version / 7) + 2;
    result -= (25 * numAlign - 10) * numAlign - 55;
    if (version >= 7) result -= 36;
  }
  return result;
}

function getNumDataCodewords(version, eclIndex) {
  return Math.floor(getNumRawDataModules(version) / 8)
    - ECC_CODEWORDS_PER_BLOCK[version][eclIndex]
      * NUM_ERROR_CORRECTION_BLOCKS[version][eclIndex];
}

function appendBits(val, len, bb) {
  for (let i = len - 1; i >= 0; i--) {
    bb.push((val >>> i) & 1);
  }
}

function getAlignmentPatternPositions(version) {
  if (version === 1) return [];
  const numAlign = Math.floor(version / 7) + 2;
  const step = version === 32 ? 26 : Math.ceil((version * 4 + 4) / (numAlign * 2 - 2)) * 2;
  const result = [6];
  for (let pos = version * 4 + 10; result.length < numAlign; pos -= step) {
    result.splice(1, 0, pos);
  }
  return result;
}

function setFunctionModule(x, y, isDark, modules, isFunction) {
  modules[y][x] = isDark;
  isFunction[y][x] = true;
}

function drawFinderPattern(x, y, modules, isFunction) {
  for (let dy = -4; dy <= 4; dy++) {
    for (let dx = -4; dx <= 4; dx++) {
      const dist = Math.max(Math.abs(dx), Math.abs(dy));
      const xx = x + dx;
      const yy = y + dy;
      if (0 <= xx && xx < modules.length && 0 <= yy && yy < modules.length) {
        setFunctionModule(xx, yy, dist !== 2 && dist !== 4, modules, isFunction);
      }
    }
  }
}

function drawAlignmentPattern(x, y, modules, isFunction) {
  for (let dy = -2; dy <= 2; dy++) {
    for (let dx = -2; dx <= 2; dx++) {
      setFunctionModule(x + dx, y + dy, Math.max(Math.abs(dx), Math.abs(dy)) !== 1, modules, isFunction);
    }
  }
}

function drawFormatBits(mask, modules, isFunction, eclBits) {
  const data = (eclBits << 3) | mask;
  let rem = data;
  for (let i = 0; i < 10; i++) {
    rem = (rem << 1) ^ ((rem >>> 9) * 0x537);
  }
  const bits = ((data << 10) | rem) ^ 0x5412;
  const size = modules.length;
  for (let i = 0; i <= 5; i++) setFunctionModule(8, i, ((bits >>> i) & 1) !== 0, modules, isFunction);
  setFunctionModule(8, 7, ((bits >>> 6) & 1) !== 0, modules, isFunction);
  setFunctionModule(8, 8, ((bits >>> 7) & 1) !== 0, modules, isFunction);
  setFunctionModule(7, 8, ((bits >>> 8) & 1) !== 0, modules, isFunction);
  for (let i = 9; i < 15; i++) setFunctionModule(14 - i, 8, ((bits >>> i) & 1) !== 0, modules, isFunction);
  for (let i = 0; i < 8; i++) setFunctionModule(size - 1 - i, 8, ((bits >>> i) & 1) !== 0, modules, isFunction);
  for (let i = 8; i < 15; i++) setFunctionModule(8, size - 15 + i, ((bits >>> i) & 1) !== 0, modules, isFunction);
  setFunctionModule(8, size - 8, true, modules, isFunction);
}

function drawCodewords(data, modules, isFunction) {
  const size = modules.length;
  let i = 0;
  for (let right = size - 1; right >= 1; right -= 2) {
    if (right === 6) right = 5;
    for (let vert = 0; vert < size; vert++) {
      for (let j = 0; j < 2; j++) {
        const x = right - j;
        const upward = ((right + 1) & 2) === 0;
        const y = upward ? size - 1 - vert : vert;
        if (!isFunction[y][x] && i < data.length * 8) {
          modules[y][x] = ((data[i >>> 3] >>> (7 - (i & 7))) & 1) !== 0;
          i++;
        }
      }
    }
  }
}

function applyMask(mask, modules, isFunction) {
  const size = modules.length;
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      if (isFunction[y][x]) continue;
      let invert = false;
      switch (mask) {
        case 0: invert = (x + y) % 2 === 0; break;
        case 1: invert = y % 2 === 0; break;
        case 2: invert = x % 3 === 0; break;
        case 3: invert = (x + y) % 3 === 0; break;
        case 4: invert = (Math.floor(x / 3) + Math.floor(y / 2)) % 2 === 0; break;
        case 5: invert = ((x * y) % 2) + ((x * y) % 3) === 0; break;
        case 6: invert = (((x * y) % 2) + ((x * y) % 3)) % 2 === 0; break;
        case 7: invert = (((x + y) % 2) + ((x * y) % 3)) % 2 === 0; break;
        default: break;
      }
      if (invert) modules[y][x] = !modules[y][x];
    }
  }
}

// Versions 7+ reserve two 18-bit version blocks. getNumRawDataModules already
// subtracts those 36 modules, so they must also be drawn and flagged as function
// modules or codewords would be written over them and no scanner could decode.
function drawVersionBits(version, modules, isFunction) {
  if (version < 7) return;
  let rem = version;
  for (let i = 0; i < 12; i++) {
    rem = (rem << 1) ^ ((rem >>> 11) * 0x1f25);
  }
  const bits = (version << 12) | rem;
  const size = modules.length;
  for (let i = 0; i < 18; i++) {
    const bit = ((bits >>> i) & 1) !== 0;
    const a = size - 11 + (i % 3);
    const b = Math.floor(i / 3);
    setFunctionModule(a, b, bit, modules, isFunction);
    setFunctionModule(b, a, bit, modules, isFunction);
  }
}

// ISO/IEC 18004 requires choosing the mask with the lowest penalty score.
// A hard-coded mask leaves large uniform regions and finder-like runs that
// many phone scanners reject, so all eight candidates are scored.
function getPenaltyScore(modules) {
  const size = modules.length;
  let result = 0;
  const N1 = 3;
  const N2 = 3;
  const N3 = 40;
  const N4 = 10;

  for (let y = 0; y < size; y++) {
    let runColor = false;
    let runLength = 0;
    const runHistory = new Array(7).fill(0);
    for (let x = 0; x < size; x++) {
      if (modules[y][x] === runColor) {
        runLength++;
        if (runLength === 5) result += N1;
        else if (runLength > 5) result++;
      } else {
        finderPenaltyAddHistory(runLength, runHistory, size);
        if (!runColor) result += finderPenaltyCountPatterns(runHistory) * N3;
        runColor = modules[y][x];
        runLength = 1;
      }
    }
    result += finderPenaltyTerminateAndCount(runColor, runLength, runHistory, size) * N3;
  }
  return result + penaltyColumnsBlocksBalance(modules, N1, N2, N3, N4);
}

// Column runs, 2x2 blocks, and the dark-module balance ratio. Split from
// getPenaltyScore only to keep each function readable.
function penaltyColumnsBlocksBalance(modules, N1, N2, N3, N4) {
  const size = modules.length;
  let result = 0;

  for (let x = 0; x < size; x++) {
    let runColor = false;
    let runLength = 0;
    const runHistory = new Array(7).fill(0);
    for (let y = 0; y < size; y++) {
      if (modules[y][x] === runColor) {
        runLength++;
        if (runLength === 5) result += N1;
        else if (runLength > 5) result++;
      } else {
        finderPenaltyAddHistory(runLength, runHistory, size);
        if (!runColor) result += finderPenaltyCountPatterns(runHistory) * N3;
        runColor = modules[y][x];
        runLength = 1;
      }
    }
    result += finderPenaltyTerminateAndCount(runColor, runLength, runHistory, size) * N3;
  }

  for (let y = 0; y < size - 1; y++) {
    for (let x = 0; x < size - 1; x++) {
      const color = modules[y][x];
      if (color === modules[y][x + 1] && color === modules[y + 1][x] && color === modules[y + 1][x + 1]) {
        result += N2;
      }
    }
  }

  let dark = 0;
  for (const row of modules) {
    for (const cell of row) if (cell) dark++;
  }
  const total = size * size;
  const k = Math.ceil(Math.abs(dark * 20 - total * 10) / total) - 1;
  return result + k * N4;
}

function finderPenaltyAddHistory(currentRunLength, runHistory, size) {
  let run = currentRunLength;
  if (runHistory[0] === 0) run += size; // add light border to initial run
  runHistory.pop();
  runHistory.unshift(run);
}

function finderPenaltyCountPatterns(runHistory) {
  const n = runHistory[1];
  const core = n > 0 && runHistory[2] === n && runHistory[3] === n * 3
    && runHistory[4] === n && runHistory[5] === n;
  return (core && runHistory[0] >= n * 4 && runHistory[6] >= n ? 1 : 0)
    + (core && runHistory[6] >= n * 4 && runHistory[0] >= n ? 1 : 0);
}

function finderPenaltyTerminateAndCount(currentRunColor, currentRunLength, runHistory, size) {
  let run = currentRunLength;
  if (currentRunColor) {
    finderPenaltyAddHistory(run, runHistory, size);
    run = 0;
  }
  run += size; // add light border to final run
  finderPenaltyAddHistory(run, runHistory, size);
  return finderPenaltyCountPatterns(runHistory);
}

function encodeSegments(text, version) {
  const bytes = new TextEncoder().encode(text);
  const bb = [];
  appendBits(0b0100, 4, bb); // byte mode
  appendBits(bytes.length, version <= 9 ? 8 : 16, bb);
  for (const b of bytes) appendBits(b, 8, bb);
  return bb;
}

function addEccAndInterleave(data, version, eclIndex) {
  const numBlocks = NUM_ERROR_CORRECTION_BLOCKS[version][eclIndex];
  const blockEccLen = ECC_CODEWORDS_PER_BLOCK[version][eclIndex];
  const rawCodewords = Math.floor(getNumRawDataModules(version) / 8);
  const numShortBlocks = numBlocks - (rawCodewords % numBlocks);
  const shortBlockLen = Math.floor(rawCodewords / numBlocks);
  const blocks = [];
  const rsDiv = reedSolomonComputeDivisor(blockEccLen);
  let k = 0;
  for (let i = 0; i < numBlocks; i++) {
    const dat = data.slice(k, k + shortBlockLen - blockEccLen + (i < numShortBlocks ? 0 : 1));
    k += dat.length;
    const ecc = reedSolomonComputeRemainder(dat, rsDiv);
    if (i < numShortBlocks) dat.push(0);
    blocks.push(dat.concat(ecc));
  }
  const result = [];
  for (let i = 0; i < blocks[0].length; i++) {
    for (let j = 0; j < blocks.length; j++) {
      if (i !== shortBlockLen - blockEccLen || j >= numShortBlocks) {
        result.push(blocks[j][i]);
      }
    }
  }
  return result;
}

function createModules(version, dataCodewords, mask, eclBits) {
  const size = version * 4 + 17;
  const modules = Array.from({ length: size }, () => Array(size).fill(false));
  const isFunction = Array.from({ length: size }, () => Array(size).fill(false));

  // finders
  drawFinderPattern(3, 3, modules, isFunction);
  drawFinderPattern(size - 4, 3, modules, isFunction);
  drawFinderPattern(3, size - 4, modules, isFunction);

  // timing
  for (let i = 0; i < size; i++) {
    setFunctionModule(6, i, i % 2 === 0, modules, isFunction);
    setFunctionModule(i, 6, i % 2 === 0, modules, isFunction);
  }

  // alignments
  const alignPos = getAlignmentPatternPositions(version);
  for (let i = 0; i < alignPos.length; i++) {
    for (let j = 0; j < alignPos.length; j++) {
      if ((i === 0 && j === 0)
        || (i === 0 && j === alignPos.length - 1)
        || (i === alignPos.length - 1 && j === 0)) {
        continue;
      }
      drawAlignmentPattern(alignPos[j], alignPos[i], modules, isFunction);
    }
  }

  // format placeholder then real
  drawFormatBits(0, modules, isFunction, eclBits);
  drawVersionBits(version, modules, isFunction);
  drawCodewords(dataCodewords, modules, isFunction);

  // mask < 0 means "pick the lowest-penalty mask", as the spec requires.
  let chosen = mask;
  if (chosen < 0) {
    let minPenalty = Infinity;
    for (let candidate = 0; candidate < 8; candidate++) {
      applyMask(candidate, modules, isFunction);
      drawFormatBits(candidate, modules, isFunction, eclBits);
      const penalty = getPenaltyScore(modules);
      if (penalty < minPenalty) {
        chosen = candidate;
        minPenalty = penalty;
      }
      applyMask(candidate, modules, isFunction); // XOR is its own inverse
    }
  }
  applyMask(chosen, modules, isFunction);
  drawFormatBits(chosen, modules, isFunction, eclBits);
  return modules;
}

/**
 * Encode text into a QR module matrix (boolean[][]).
 * Uses ECC level M and automatic version 1–10.
 */
export function encodeQRModules(text) {
  const value = String(text || "");
  if (!value) throw new Error("empty QR payload");
  if (value.length > 500) throw new Error("QR payload too long");

  const eclIndex = 1; // M
  const eclBits = 0b00; // M in format bits

  for (let version = 1; version <= 10; version++) {
    const dataCapacity = getNumDataCodewords(version, eclIndex) * 8;
    const bb = encodeSegments(value, version);
    // Reject before computing the terminator. Deriving it with Math.min made it
    // negative on overflow, which silently passed the guard, no-oped appendBits,
    // and truncated the payload inside addEccAndInterleave: the QR rendered but
    // decoded to a cut-off URL.
    if (bb.length > dataCapacity) continue;
    const terminator = Math.min(4, dataCapacity - bb.length);
    appendBits(0, terminator, bb);
    while (bb.length % 8 !== 0) bb.push(0);
    const dataCodewords = [];
    for (let i = 0; i < bb.length; i += 8) {
      let b = 0;
      for (let j = 0; j < 8; j++) b = (b << 1) | bb[i + j];
      dataCodewords.push(b);
    }
    const capacityBytes = getNumDataCodewords(version, eclIndex);
    for (let i = 0; dataCodewords.length < capacityBytes; i++) {
      dataCodewords.push(i % 2 === 0 ? 0xec : 0x11);
    }
    const allCodewords = addEccAndInterleave(dataCodewords, version, eclIndex);
    return createModules(version, allCodewords, -1, eclBits);
  }
  throw new Error("QR payload exceeds supported versions");
}

/**
 * Render QR as an SVG string (black modules on white).
 * margin defaults to the 4-module quiet zone the spec requires; smaller values
 * make phone scanners miss the finder patterns.
 */
export function qrToSvg(text, { size = 180, margin = 4, dark = "#111", light = "#fff" } = {}) {
  const modules = encodeQRModules(text);
  const n = modules.length;
  const scale = size / (n + margin * 2);
  const total = (n + margin * 2) * scale;
  let path = "";
  for (let y = 0; y < n; y++) {
    for (let x = 0; x < n; x++) {
      if (!modules[y][x]) continue;
      const px = (x + margin) * scale;
      const py = (y + margin) * scale;
      path += `M${px.toFixed(2)} ${py.toFixed(2)}h${scale.toFixed(2)}v${scale.toFixed(2)}h${(-scale).toFixed(2)}z`;
    }
  }
  return `<svg xmlns="http://www.w3.org/2000/svg" width="${Math.round(total)}" height="${Math.round(total)}" viewBox="0 0 ${total} ${total}" role="img" aria-label="QR code"><rect width="100%" height="100%" fill="${light}"/><path fill="${dark}" d="${path}"/></svg>`;
}
