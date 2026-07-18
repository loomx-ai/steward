import assert from "node:assert/strict";
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import zlib from "node:zlib";
import { fileURLToPath } from "node:url";

const webRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const projectRoot = path.resolve(webRoot, "..");
const catalogPath = path.join(
  projectRoot,
  "providers",
  "alicloud",
  "resourcecenter",
  "resources.json",
);

test("every Alibaba Cloud instance resource has one safe local icon", () => {
  const catalog = JSON.parse(fs.readFileSync(catalogPath, "utf8"));
  assert.equal(catalog.resources.length, 170);
  const instances = catalog.resources.filter(
    (resource) => resource.level === "instance",
  );
  const subresources = catalog.resources.filter(
    (resource) => resource.level === "subresource",
  );
  assert.equal(instances.length, 137);
  assert.equal(subresources.length, 33);

  const seenPaths = new Set();
  const seenDigests = new Map();
  const failures = [];
  for (const resource of instances) {
    try {
      const expectedExtension =
        resource.icon_origin === "official" ? ".svg" : ".png";
      const expectedPath = `/icons/alicloud/${resource.native_type
        .toLowerCase()
        .replaceAll("::", "-")}${expectedExtension}`;
      assert.equal(resource.icon, expectedPath);
      assert.ok(!seenPaths.has(resource.icon), `duplicate ${resource.icon}`);
      seenPaths.add(resource.icon);

      const filePath = path.join(
        webRoot,
        "public",
        resource.icon.replace(/^\//, ""),
      );
      assert.ok(fs.existsSync(filePath), `missing ${filePath}`);
      const digest = crypto
        .createHash("sha256")
        .update(fs.readFileSync(filePath))
        .digest("hex");
      assert.ok(
        !seenDigests.has(digest),
        `icon content duplicates ${seenDigests.get(digest)}`,
      );
      seenDigests.set(digest, resource.native_type);
      if (expectedExtension === ".svg") {
        validateSvg(filePath);
      } else {
        validateTransparentPng(filePath);
      }
    } catch (error) {
      failures.push(`${resource.native_type}: ${error.message}`);
    }
  }
  assert.deepEqual(failures, []);
});

function validateSvg(filePath) {
  const source = fs.readFileSync(filePath, "utf8");
  assert.match(source, /<svg\b/i);
  assert.doesNotMatch(
    source,
    /<script\b|<foreignObject\b|<!DOCTYPE|on[a-z]+\s*=|(?:href|src)\s*=\s*["'](?:https?:|\/\/)|url\(\s*["']?(?:https?:|\/\/)/i,
  );
  assert.doesNotMatch(
    source,
    /\b(?:fill|stroke)=["'](?!#000000|none|currentColor)[^"']+/i,
  );
}

function validateTransparentPng(filePath) {
  const source = fs.readFileSync(filePath);
  assert.deepEqual(
    source.subarray(0, 8),
    Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]),
  );
  const chunks = readPngChunks(source);
  const ihdr = chunks.find((chunk) => chunk.type === "IHDR")?.data;
  assert.ok(ihdr, "missing IHDR");
  const width = ihdr.readUInt32BE(0);
  const height = ihdr.readUInt32BE(4);
  const bitDepth = ihdr[8];
  const colorType = ihdr[9];
  const interlace = ihdr[12];
  assert.ok(width > 0 && height > 0 && width === height, "PNG must be square");
  assert.equal(bitDepth, 8, "PNG must use 8-bit channels");
  assert.ok(
    colorType === 4 || colorType === 6,
    "PNG must contain greyscale-alpha or RGBA pixels",
  );
  assert.equal(interlace, 0, "PNG must be non-interlaced for validation");

  const bytesPerPixel = colorType === 6 ? 4 : 2;
  const pixels = decodePngPixels(chunks, width, height, bytesPerPixel);
  const alphaOffset = bytesPerPixel - 1;
  const alphaAt = (x, y) =>
    pixels[(y * width + x) * bytesPerPixel + alphaOffset];
  for (const [x, y] of [
    [0, 0],
    [width - 1, 0],
    [0, height - 1],
    [width - 1, height - 1],
  ]) {
    assert.ok(alphaAt(x, y) <= 8, `corner ${x},${y} is not transparent`);
  }
  let occupied = 0;
  for (let y = 0; y < height; y += 1) {
    for (let x = 0; x < width; x += 1) {
      if (alphaAt(x, y) > 24) occupied += 1;
      const pixelOffset = (y * width + x) * bytesPerPixel;
      const colorChannelCount = bytesPerPixel - 1;
      for (let channel = 0; channel < colorChannelCount; channel += 1) {
        assert.equal(
          pixels[pixelOffset + channel],
          0,
          `pixel ${x},${y} is not pure black`,
        );
      }
    }
  }
  const coverage = occupied / (width * height);
  assert.ok(
    coverage >= 0.08 && coverage <= 0.75,
    `subject coverage ${coverage.toFixed(3)} is implausible`,
  );
}

function readPngChunks(source) {
  const chunks = [];
  let offset = 8;
  while (offset + 12 <= source.length) {
    const length = source.readUInt32BE(offset);
    const type = source.toString("ascii", offset + 4, offset + 8);
    const dataStart = offset + 8;
    const dataEnd = dataStart + length;
    assert.ok(dataEnd + 4 <= source.length, `truncated PNG chunk ${type}`);
    chunks.push({ type, data: source.subarray(dataStart, dataEnd) });
    offset = dataEnd + 4;
    if (type === "IEND") break;
  }
  return chunks;
}

function decodePngPixels(chunks, width, height, bytesPerPixel) {
  const compressed = Buffer.concat(
    chunks.filter((chunk) => chunk.type === "IDAT").map((chunk) => chunk.data),
  );
  assert.ok(compressed.length > 0, "missing IDAT");
  const inflated = zlib.inflateSync(compressed);
  const stride = width * bytesPerPixel;
  assert.equal(inflated.length, height * (stride + 1));
  const result = Buffer.alloc(width * height * bytesPerPixel);
  let sourceOffset = 0;
  for (let y = 0; y < height; y += 1) {
    const filter = inflated[sourceOffset];
    sourceOffset += 1;
    const rowOffset = y * stride;
    for (let x = 0; x < stride; x += 1) {
      const raw = inflated[sourceOffset + x];
      const left =
        x >= bytesPerPixel ? result[rowOffset + x - bytesPerPixel] : 0;
      const above = y > 0 ? result[rowOffset + x - stride] : 0;
      const upperLeft =
        y > 0 && x >= bytesPerPixel
          ? result[rowOffset + x - stride - bytesPerPixel]
          : 0;
      let value;
      switch (filter) {
        case 0:
          value = raw;
          break;
        case 1:
          value = raw + left;
          break;
        case 2:
          value = raw + above;
          break;
        case 3:
          value = raw + Math.floor((left + above) / 2);
          break;
        case 4:
          value = raw + paeth(left, above, upperLeft);
          break;
        default:
          assert.fail(`unsupported PNG filter ${filter}`);
      }
      result[rowOffset + x] = value & 0xff;
    }
    sourceOffset += stride;
  }
  return result;
}

function paeth(left, above, upperLeft) {
  const estimate = left + above - upperLeft;
  const leftDistance = Math.abs(estimate - left);
  const aboveDistance = Math.abs(estimate - above);
  const upperLeftDistance = Math.abs(estimate - upperLeft);
  if (leftDistance <= aboveDistance && leftDistance <= upperLeftDistance) {
    return left;
  }
  if (aboveDistance <= upperLeftDistance) return above;
  return upperLeft;
}
