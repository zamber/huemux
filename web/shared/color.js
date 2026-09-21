// xyToRgb — client-side inverse of internal/lightctl/color.go's rgbToXY
// (standard sRGB D65 Hue gamut conversion). Shared between lights.js (card
// tinting, scene swatches) and app.js (the sync page's scenes strip) so both
// render scene-swatch colors identically.
function xyToRgb(x, y, briPct) {
  const bri = briPct !== undefined ? Math.max(0.08, briPct / 100) : 1;
  const yy = y === 0 ? 0.0001 : y;
  const Y = bri;
  const X = (Y / yy) * x;
  const Z = (Y / yy) * (1 - x - y);

  let r = X * 1.656492 - Y * 0.354851 - Z * 0.255038;
  let g = -X * 0.707196 + Y * 1.655397 + Z * 0.036152;
  let b = X * 0.051713 - Y * 0.121364 + Z * 1.011530;

  const gamma = (c) => (c <= 0.0031308 ? 12.92 * c : 1.055 * Math.pow(c, 1 / 2.4) - 0.055);
  r = gamma(r); g = gamma(g); b = gamma(b);

  const max = Math.max(r, g, b, 0.0001);
  if (max > 1) { r /= max; g /= max; b /= max; }

  const clamp = (c) => Math.max(0, Math.min(255, Math.round(c * 255)));
  return [clamp(r), clamp(g), clamp(b)];
}

// kelvinToRgb — approximate white-body color for a given Kelvin temperature
// (Tanner Helland's fit, clamped to 1000-40000 K). Display-only: the bridge
// itself receives color temperature as mirek via CLIP v2, never as an RGB
// conversion. Used by lights.js for the picker's white-temperature strip and
// for tinting CT-mode light cards.
function kelvinToRgb(kelvin) {
  const k = Math.max(1000, Math.min(40000, kelvin)) / 100;
  let r, g, b;
  if (k <= 66) {
    r = 255;
    g = 99.4708025861 * Math.log(k) - 161.1195681661;
    b = k <= 19 ? 0 : 138.5177312231 * Math.log(k - 10) - 305.0447927307;
  } else {
    r = 329.698727446 * Math.pow(k - 60, -0.1332047592);
    g = 288.1221695283 * Math.pow(k - 60, -0.0755148492);
    b = 255;
  }
  const clamp = (c) => Math.max(0, Math.min(255, Math.round(c)));
  return [clamp(r), clamp(g), clamp(b)];
}
