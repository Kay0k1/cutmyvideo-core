/** Shared delivery vocabulary; intentionally independent of response adapters. */
const object = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value);
/** Reject malformed new options before JSON copying can erase their meaning. */
export function deliveryOptions(options: Record<string, unknown>): void {
  const speed = options.speed;
  if (speed !== undefined && speed !== null && (!object(speed) || Object.keys(speed).sort().join(",") !== "denominator,numerator" || ![speed.numerator,speed.denominator].every(v => Number.isSafeInteger(v) && Number(v) >= 1 && Number(v) <= 1000) || Number(speed.numerator) * 2 < Number(speed.denominator) || Number(speed.numerator) > Number(speed.denominator) * 2)) throw new TypeError("speed requires numerator/denominator1..1000 and ratio0.5..2");
  const size = options.target_size_bytes;
  if (size !== undefined && (!Number.isSafeInteger(size) || Number(size) < 0)) throw new TypeError("target_size_bytes must be a nonnegative safe integer");
  for (const field of ["clear_speed","clear_target_size"]) if (options[field] !== undefined && typeof options[field] !== "boolean") throw new TypeError(field + " must be boolean");
  if (options.clear_speed === true && speed !== undefined && speed !== null || options.clear_target_size === true && size !== undefined && size !== 0) throw new TypeError("A delivery option cannot be set and cleared together");
}
export function deliveryResponse(value: unknown): boolean {
  if (!object(value)) return false;
  try { deliveryOptions(value); return true; } catch { return false; }
}
