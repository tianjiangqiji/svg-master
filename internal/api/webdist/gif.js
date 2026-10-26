/* gif.js — 极简 GIF89a 编码器（零依赖，纯前端）。
 *
 * 用法：
 *   var enc = new GIFEncoder(w, h, { loop: 0 });   // loop 0 = 无限循环
 *   enc.addFrame(imageData.data, delayCs);          // RGBA Uint8ClampedArray + 延迟（厘秒）
 *   var blob = enc.finish();                        // → Blob('image/gif')
 *
 * 设计取舍：
 *   - 从全部帧用中位切分（median cut）建全局调色板，每帧映射到最近色；
 *     动图各帧色调通常一致，全局调色板能让文件更小、切换更稳。
 *   - 单帧内部不再做抖动（dithering）：SVG 转出来的画面本身是大色块，
 *     抖动只会让 GIF 变大、边缘变脏。
 *   - LZW 按 GIF 规范实现：最小码长 ≥2、码表上限 4096、满表发 clear code。
 *   - 全部用 Uint8Array 拼装，不用字符串累加（大图会卡）。
 */
(function (global) {
  'use strict';

  // ---------- 调色板：中位切分 ----------

  /* buildPalette 从若干帧的 RGBA 采样出 ≤maxColors 的调色板。
   * pixels 是若干个 Uint8ClampedArray（每 4 字节一个像素）。
   * 返回 { colors: [[r,g,b], ...], transparentIndex: -1 }。
   * 半透明/全透明像素不参与统计（它们在索引里单独占一个透明色）。 */
  function buildPalette(samples, maxColors) {
    var buckets = [];
    var all = [];
    var i, n, p;
    for (i = 0; i < samples.length; i++) {
      var px = samples[i];
      n = px.length / 4;
      // 大图按步长抽样，控制建色表的时间
      var step = n > 20000 ? Math.ceil(n / 20000) : 1;
      for (p = 0; p < n; p += step) {
        var o = p * 4;
        if (px[o + 3] < 128) continue; // 透明像素不参与调色
        all.push([px[o], px[o + 1], px[o + 2]]);
      }
    }
    if (!all.length) return { colors: [[0, 0, 0]], transparentIndex: -1 };

    buckets.push(all);
    while (buckets.length < maxColors) {
      // 找「颜色跨度最大」的桶来切
      var bi = -1, bestRange = -1, bestCh = 0;
      for (i = 0; i < buckets.length; i++) {
        var b = buckets[i];
        if (b.length < 2) continue;
        var rng = channelRange(b);
        if (rng.range > bestRange) {
          bestRange = rng.range;
          bestCh = rng.channel;
          bi = i;
        }
      }
      if (bi < 0 || bestRange <= 0) break;
      var target = buckets[bi];
      target.sort(function (a, b2) { return a[bestCh] - b2[bestCh]; });
      var mid = target.length >> 1;
      buckets.splice(bi, 1, target.slice(0, mid), target.slice(mid));
    }

    var colors = buckets.map(function (b) {
      var r = 0, g = 0, bl = 0;
      for (var k = 0; k < b.length; k++) {
        r += b[k][0]; g += b[k][1]; bl += b[k][2];
      }
      var c = b.length || 1;
      return [Math.round(r / c), Math.round(g / c), Math.round(bl / c)];
    });
    return { colors: colors, transparentIndex: -1 };
  }

  function channelRange(bucket) {
    var mn = [255, 255, 255], mx = [0, 0, 0];
    for (var i = 0; i < bucket.length; i++) {
      for (var c = 0; c < 3; c++) {
        var v = bucket[i][c];
        if (v < mn[c]) mn[c] = v;
        if (v > mx[c]) mx[c] = v;
      }
    }
    var range = 0, channel = 0;
    for (var c2 = 0; c2 < 3; c2++) {
      var d = mx[c2] - mn[c2];
      if (d > range) { range = d; channel = c2; }
    }
    return { range: range, channel: channel };
  }

  // ---------- 最近色查找（带缓存）----------
  function makeMapper(colors, transparentIndex) {
    var cache = new Map();
    var n = colors.length;
    return function (r, g, b, a) {
      if (a < 128) return transparentIndex >= 0 ? transparentIndex : 0;
      var key = (r << 16) | (g << 8) | b;
      var hit = cache.get(key);
      if (hit !== undefined) return hit;
      var best = 0, bestD = Infinity;
      for (var i = 0; i < n; i++) {
        if (i === transparentIndex) continue; // 不把不透明的深色像素映射为透明色
        var c = colors[i];
        var dr = r - c[0], dg = g - c[1], db = b - c[2];
        var d = dr * dr + dg * dg + db * db;
        if (d < bestD) { bestD = d; best = i; if (d === 0) break; }
      }
      if (cache.size < 65536) cache.set(key, best);
      return best;
    };
  }

  // ---------- 位流与 LZW ----------

  function ByteBuf() {
    this.chunks = [];
    this.cur = new Uint8Array(65536);
    this.len = 0;
  }
  ByteBuf.prototype.byte = function (v) {
    if (this.len === this.cur.length) {
      this.chunks.push(this.cur);
      this.cur = new Uint8Array(65536);
      this.len = 0;
    }
    this.cur[this.len++] = v & 0xff;
  };
  ByteBuf.prototype.bytes = function (arr) {
    for (var i = 0; i < arr.length; i++) this.byte(arr[i]);
  };
  ByteBuf.prototype.u16 = function (v) {
    this.byte(v & 0xff);
    this.byte((v >> 8) & 0xff);
  };
  ByteBuf.prototype.ascii = function (s) {
    for (var i = 0; i < s.length; i++) this.byte(s.charCodeAt(i));
  };
  ByteBuf.prototype.take = function () {
    var out = new Uint8Array(this.total());
    var off = 0, i;
    for (i = 0; i < this.chunks.length; i++) {
      out.set(this.chunks[i], off);
      off += this.chunks[i].length;
    }
    out.set(this.cur.subarray(0, this.len), off);
    return out;
  };
  ByteBuf.prototype.total = function () {
    var t = this.len;
    for (var i = 0; i < this.chunks.length; i++) t += this.chunks[i].length;
    return t;
  };

  /* lzwEncode 按 GIF 变体 LZW 压缩索引流。
   * minCodeSize 是颜色位深（≥2）；返回原始字节（不含块头/分块）。 */
  function lzwEncode(indices, minCodeSize) {
    var clearCode = 1 << minCodeSize;
    var eoiCode = clearCode + 1;
    var nextCode = eoiCode + 1;
    var codeSize = minCodeSize + 1;

    var out = [];
    var cur = 0, curBits = 0;
    function emit(code) {
      cur |= code << curBits;
      curBits += codeSize;
      while (curBits >= 8) {
        out.push(cur & 0xff);
        cur >>= 8;
        curBits -= 8;
      }
    }

    // 初始码表：单色
    var dict = new Map();
    function resetDict() {
      dict = new Map();
      nextCode = eoiCode + 1;
      codeSize = minCodeSize + 1;
    }

    emit(clearCode);
    if (!indices.length) {
      emit(eoiCode);
      if (curBits > 0) out.push(cur & 0xff);
      return out;
    }

    var prefix = indices[0];
    for (var i = 1; i < indices.length; i++) {
      var k = indices[i];
      var key = (prefix << 8) | k; // 调色板 ≤256，k 一定在 0..255
      var found = dict.get(key);
      if (found !== undefined) {
        prefix = found;
        continue;
      }
      emit(prefix);
      if (nextCode < 4096) {
        dict.set(key, nextCode++);
        if (nextCode > (1 << codeSize) && codeSize < 12) codeSize++;
      } else {
        emit(clearCode);
        resetDict();
      }
      prefix = k;
    }
    emit(prefix);
    emit(eoiCode);
    if (curBits > 0) out.push(cur & 0xff);
    return out;
  }

  /* 把原始字节切成 GIF 子块（≤255 字节一块，0 结尾）。 */
  function writeSubBlocks(buf, data) {
    var off = 0;
    while (off < data.length) {
      var n = Math.min(255, data.length - off);
      buf.byte(n);
      for (var i = 0; i < n; i++) buf.byte(data[off + i]);
      off += n;
    }
    buf.byte(0);
  }

  // ---------- 编码器 ----------

  function GIFEncoder(width, height, opts) {
    opts = opts || {};
    this.w = Math.max(1, width | 0);
    this.h = Math.max(1, height | 0);
    this.loop = opts.loop === undefined ? 0 : (opts.loop | 0);
    this.frames = [];
    this.palette = null;
    this.mapper = null;
    this.transparentIndex = -1;
  }

  /* addFrame 接收完整 RGBA 帧（Uint8ClampedArray / Uint8Array）与延迟（厘秒）。 */
  GIFEncoder.prototype.addFrame = function (rgba, delayCs) {
    if (!rgba || rgba.length < this.w * this.h * 4) return false;
    var d = delayCs === undefined ? 10 : Math.max(1, delayCs | 0);
    this.frames.push({ data: rgba, delay: d });
    return true;
  };

  /* finish 编码全部帧，返回 Blob。没有帧时返回 null。 */
  GIFEncoder.prototype.finish = function () {
    if (!this.frames.length) return null;

    // 1) 全局调色板：留 1 个槽给透明色（若有透明像素）
    var hasAlpha = false;
    for (var f = 0; f < this.frames.length && !hasAlpha; f++) {
      var px = this.frames[f].data;
      for (var i = 3; i < px.length; i += 4) {
        if (px[i] < 128) { hasAlpha = true; break; }
      }
    }
    var maxColors = hasAlpha ? 255 : 256;
    var pal = buildPalette(this.frames.map(function (fr) { return fr.data; }), maxColors);
    var colors = pal.colors;
    this.transparentIndex = hasAlpha ? colors.length : -1;
    if (hasAlpha) colors = colors.concat([[0, 0, 0]]);

    // 色表大小必须是 2 的幂
    var bits = 1;
    while ((1 << bits) < colors.length) bits++;
    if (bits < 1) bits = 1;
    var tableSize = 1 << bits;
    var minCodeSize = Math.max(2, bits);

    this.mapper = makeMapper(colors, this.transparentIndex);

    var buf = new ByteBuf();
    // ---- Header ----
    buf.ascii('GIF89a');
    // ---- Logical Screen Descriptor ----
    buf.u16(this.w);
    buf.u16(this.h);
    // 全局色表标志 | 颜色分辨率(7) | 排序(0) | 色表大小
    buf.byte(0x80 | 0x70 | (bits - 1));
    buf.byte(hasAlpha ? this.transparentIndex : 0); // 透明帧清除到透明背景
    buf.byte(0); // 像素宽高比
    // ---- 全局色表 ----
    for (var c = 0; c < tableSize; c++) {
      var col = colors[c] || [0, 0, 0];
      buf.byte(col[0]); buf.byte(col[1]); buf.byte(col[2]);
    }
    // ---- 循环扩展 ----
    buf.byte(0x21); buf.byte(0xff); buf.byte(0x0b);
    buf.ascii('NETSCAPE2.0');
    buf.byte(0x03); buf.byte(0x01);
    buf.u16(this.loop);
    buf.byte(0x00);

    var total = this.w * this.h;
    for (var fi = 0; fi < this.frames.length; fi++) {
      var fr = this.frames[fi];
      // 索引化
      var idx = new Uint8Array(total);
      var src = fr.data;
      var map = this.mapper;
      for (var p = 0, o = 0; p < total; p++, o += 4) {
        idx[p] = map(src[o], src[o + 1], src[o + 2], src[o + 3]);
      }
      // ---- Graphic Control Extension ----
      buf.byte(0x21); buf.byte(0xf9); buf.byte(0x04);
      // 输入是完整帧：透明背景必须先清除上一帧，避免残影累积成静态画面。
      var disposal = hasAlpha ? 2 : 1;
      var flags = (disposal << 2) | (this.transparentIndex >= 0 ? 1 : 0);
      buf.byte(flags);
      buf.u16(fr.delay);
      buf.byte(this.transparentIndex >= 0 ? this.transparentIndex : 0);
      buf.byte(0x00);
      // ---- Image Descriptor ----
      buf.byte(0x2c);
      buf.u16(0); buf.u16(0);
      buf.u16(this.w); buf.u16(this.h);
      buf.byte(0x00); // 无局部色表、非交错
      // ---- LZW 数据 ----
      buf.byte(minCodeSize);
      writeSubBlocks(buf, lzwEncode(idx, minCodeSize));
    }
    buf.byte(0x3b); // Trailer

    return new Blob([buf.take()], { type: 'image/gif' });
  };

  // ---------- 导出 ----------

  var api = { GIFEncoder: GIFEncoder, buildPalette: buildPalette };
  global.GIFEncoder = GIFEncoder;
  global.GIFEncoderLib = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof window !== 'undefined' ? window : this);
