//@rt rtFail
function rtFail(path: string, reason: string): never {
  throw new ValidationError(path, reason);
}

//@rt rtUnknown
function rtUnknown(_value: unknown, _path: string): void {}

//@rt rtObject
function rtObject(value: unknown, path: string): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    rtFail(path, "not an object");
  }

  return value as Record<string, unknown>;
}

//@rt rtArray
function rtArray(value: unknown, path: string): readonly unknown[] {
  if (!Array.isArray(value)) {
    rtFail(path, "not an array");
  }

  return value;
}

//@rt rtString
function rtString(value: unknown, path: string): string {
  if (typeof value !== "string") {
    rtFail(path, "not a string");
  }

  return value;
}

//@rt rtNumber
function rtNumber(value: unknown, path: string): number {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    rtFail(path, "not a number");
  }

  return value;
}

//@rt rtInteger
function rtInteger(value: unknown, path: string): number {
  if (typeof value !== "number" || !Number.isInteger(value)) {
    rtFail(path, "not an integer");
  }

  return value;
}

//@rt rtInt32
const rtInt32Min = -2147483648;
const rtInt32Max = 2147483647;

function rtInt32(value: unknown, path: string): number {
  const integer = rtInteger(value, path);
  if (integer < rtInt32Min || integer > rtInt32Max) {
    rtFail(path, "out of the int32 range");
  }

  return integer;
}

//@rt rtBoolean
function rtBoolean(value: unknown, path: string): boolean {
  if (typeof value !== "boolean") {
    rtFail(path, "not a boolean");
  }

  return value;
}

//@rt rtDateTime
const rtDateTimePattern = new RegExp(
  "^\\d{4}-\\d{2}-\\d{2}[Tt]\\d{2}:\\d{2}:\\d{2}(\\.\\d+)?([Zz]|[+-]\\d{2}:\\d{2})$",
);

function rtDateTime(value: unknown, path: string): string {
  const text = rtString(value, path);
  if (!rtDateTimePattern.test(text) || Number.isNaN(Date.parse(text))) {
    rtFail(path, "not an RFC 3339 date-time");
  }

  return text;
}

//@rt rtRequire
function rtRequire(
  path: string,
  object: Record<string, unknown>,
  names: readonly string[],
): void {
  for (const name of names) {
    if (!Object.prototype.hasOwnProperty.call(object, name)) {
      rtFail(path, `missing required property "${name}"`);
    }
  }
}

//@rt rtKnownKeys
function rtKnownKeys(
  path: string,
  object: Record<string, unknown>,
  names: readonly string[],
): void {
  for (const key of Object.keys(object)) {
    if (!names.includes(key)) {
      rtFail(path, `unknown property "${key}"`);
    }
  }
}

//@rt rtDependent
function rtDependent(
  path: string,
  object: Record<string, unknown>,
  property: string,
  requires: readonly string[],
): void {
  if (!Object.prototype.hasOwnProperty.call(object, property)) {
    return;
  }

  for (const name of requires) {
    if (!Object.prototype.hasOwnProperty.call(object, name)) {
      rtFail(path, `"${property}" needs "${name}"`);
    }
  }
}

//@rt rtCanonical
function rtCanonical(value: unknown): string {
  if (Array.isArray(value)) {
    return `[${value.map(rtCanonical).join(",")}]`;
  }

  if (typeof value === "object" && value !== null) {
    const record = value as Record<string, unknown>;
    const members = Object.keys(record)
      .sort()
      .map((key) => `${JSON.stringify(key)}:${rtCanonical(record[key])}`);

    return `{${members.join(",")}}`;
  }

  return JSON.stringify(value);
}

//@rt rtConst
function rtConst(path: string, value: unknown, expected: unknown): void {
  if (rtCanonical(value) !== rtCanonical(expected)) {
    rtFail(path, `want ${JSON.stringify(expected)}`);
  }
}

//@rt rtEnum
function rtEnum(
  path: string,
  value: unknown,
  values: readonly unknown[],
): void {
  if (!values.includes(value)) {
    rtFail(path, `unknown value ${JSON.stringify(value)}`);
  }
}

//@rt rtMinLength
function rtMinLength(path: string, value: string, limit: number): void {
  if (Array.from(value).length < limit) {
    rtFail(path, `shorter than ${limit} characters`);
  }
}

//@rt rtMaxLength
function rtMaxLength(path: string, value: string, limit: number): void {
  if (Array.from(value).length > limit) {
    rtFail(path, `longer than ${limit} characters`);
  }
}

//@rt rtPattern
const rtPatterns = new Map<string, RegExp>();

function rtPattern(path: string, value: string, pattern: string): void {
  let compiled = rtPatterns.get(pattern);
  if (compiled === undefined) {
    try {
      compiled = new RegExp(pattern, "u");
    } catch (err) {
      rtFail(path, `pattern ${JSON.stringify(pattern)}: ${String(err)}`);
    }

    rtPatterns.set(pattern, compiled);
  }

  if (!compiled.test(value)) {
    rtFail(path, `does not match ${JSON.stringify(pattern)}`);
  }
}

//@rt rtKeyPattern
function rtKeyPattern(
  path: string,
  object: Record<string, unknown>,
  pattern: string,
): void {
  for (const key of Object.keys(object)) {
    rtPattern(`${path}.${key}`, key, pattern);
  }
}

//@rt rtMinimum
function rtMinimum(path: string, value: number, limit: number): void {
  if (value < limit) {
    rtFail(path, `less than ${limit}`);
  }
}

//@rt rtMaximum
function rtMaximum(path: string, value: number, limit: number): void {
  if (value > limit) {
    rtFail(path, `greater than ${limit}`);
  }
}

//@rt rtExclusiveMinimum
function rtExclusiveMinimum(path: string, value: number, limit: number): void {
  if (value <= limit) {
    rtFail(path, `not greater than ${limit}`);
  }
}

//@rt rtExclusiveMaximum
function rtExclusiveMaximum(path: string, value: number, limit: number): void {
  if (value >= limit) {
    rtFail(path, `not less than ${limit}`);
  }
}

//@rt rtMultipleOf
const rtMultipleOfTolerance = 1e-9;

function rtMultipleOf(path: string, value: number, divisor: number): void {
  if (divisor <= 0) {
    rtFail(path, `multipleOf ${divisor} is not positive`);
  }

  const quotient = value / divisor;
  if (Math.abs(quotient - Math.round(quotient)) > rtMultipleOfTolerance) {
    rtFail(path, `${value} is not a multiple of ${divisor}`);
  }
}

//@rt rtMinItems
function rtMinItems(path: string, count: number, limit: number): void {
  if (count < limit) {
    rtFail(path, `fewer than ${limit} items`);
  }
}

//@rt rtMaxItems
function rtMaxItems(path: string, count: number, limit: number): void {
  if (count > limit) {
    rtFail(path, `more than ${limit} items`);
  }
}

//@rt rtUniqueItems
function rtUniqueItems(path: string, items: readonly unknown[]): void {
  const seen = new Map<string, number>();

  items.forEach((item, index) => {
    const key = rtCanonical(item);
    const first = seen.get(key);
    if (first !== undefined) {
      rtFail(path, `items ${first} and ${index} are equal`);
    }

    seen.set(key, index);
  });
}

//@rt rtMinProperties
function rtMinProperties(path: string, count: number, limit: number): void {
  if (count < limit) {
    rtFail(path, `fewer than ${limit} properties`);
  }
}

//@rt rtMaxProperties
function rtMaxProperties(path: string, count: number, limit: number): void {
  if (count > limit) {
    rtFail(path, `more than ${limit} properties`);
  }
}

//@rt rtDiscriminator
function rtDiscriminator(
  path: string,
  object: Record<string, unknown>,
  property: string,
): string {
  if (!Object.prototype.hasOwnProperty.call(object, property)) {
    rtFail(path, `missing discriminator "${property}"`);
  }

  const value = object[property];
  if (typeof value === "string") {
    return value;
  }

  return JSON.stringify(value);
}

//@rt rtOneOf
function rtOneOf(path: string, variants: readonly (() => void)[]): void {
  const reasons: string[] = [];
  let matched = 0;

  for (const variant of variants) {
    try {
      variant();
      matched++;
    } catch (err) {
      if (!(err instanceof ValidationError)) {
        throw err;
      }

      reasons.push(err.message);
    }
  }

  if (matched === 0) {
    rtFail(path, `no variant matched: ${reasons.join("; ")}`);
  }

  if (matched > 1) {
    rtFail(path, `${matched} variants match`);
  }
}

//@rt rtParseJSON
function rtParseJSON(text: string, path: string): unknown {
  try {
    return JSON.parse(text) as unknown;
  } catch (err) {
    return rtFail(path, `not JSON: ${String(err)}`);
  }
}

//@rt rtFill
const rtPlaceholder = new RegExp("\\{[^}]*\\}", "g");

function rtFill(template: string, values: Record<string, string>): string {
  return template.replace(rtPlaceholder, (placeholder) => {
    const value = values[placeholder];

    return value === undefined ? placeholder : value;
  });
}

//@rt rtPointer
function rtPointer(document: unknown, pointer: string): [unknown, boolean] {
  if (pointer === "") {
    return [document, true];
  }

  let current = document;

  for (const raw of pointer.slice(1).split("/")) {
    const token = raw.split("~1").join("/").split("~0").join("~");

    if (Array.isArray(current)) {
      const index = Number(token);
      if (!Number.isInteger(index) || index < 0 || index >= current.length) {
        return [undefined, false];
      }

      current = current[index];

      continue;
    }

    if (typeof current !== "object" || current === null) {
      return [undefined, false];
    }

    const record = current as Record<string, unknown>;
    if (!Object.prototype.hasOwnProperty.call(record, token)) {
      return [undefined, false];
    }

    current = record[token];
  }

  return [current, true];
}

//@rt rtDecodeMessage
interface RtMatcher {
  readonly pointer: string;
  readonly value: unknown;
}

interface RtCandidate {
  readonly name: string;
  readonly matchers: readonly RtMatcher[];
  readonly fallback: boolean;
  readonly check: (payload: unknown) => void;
}

function rtMatchesAll(document: unknown, matchers: readonly RtMatcher[]): boolean {
  return matchers.every((matcher) => {
    const [actual, found] = rtPointer(document, matcher.pointer);

    return found && rtCanonical(actual) === rtCanonical(matcher.value);
  });
}

function rtDecodeOne(
  document: unknown,
  candidates: readonly RtCandidate[],
): string {
  const reasons: string[] = [];
  const matched: string[] = [];

  for (const candidate of candidates) {
    try {
      candidate.check(document);
      matched.push(candidate.name);
    } catch (err) {
      if (!(err instanceof ValidationError)) {
        throw err;
      }

      reasons.push(`${candidate.name}: ${err.message}`);
    }
  }

  if (matched.length > 1) {
    throw new AmbiguousMessageError(matched);
  }

  const name = matched[0];
  if (name === undefined) {
    throw new UnknownMessageError(reasons);
  }

  return name;
}

function rtDecodeMessage(
  data: string,
  candidates: readonly RtCandidate[],
): [string, unknown] {
  let document: unknown;
  try {
    document = JSON.parse(data) as unknown;
  } catch (err) {
    throw new UnknownMessageError([`message is not JSON: ${String(err)}`]);
  }

  const pinned = candidates.filter(
    (candidate) =>
      candidate.matchers.length > 0 &&
      rtMatchesAll(document, candidate.matchers),
  );
  if (pinned.length > 0) {
    return [rtDecodeOne(document, pinned), document];
  }

  const unpinned = candidates.filter(
    (candidate) => candidate.matchers.length === 0 && !candidate.fallback,
  );
  const fallbacks = candidates.filter((candidate) => candidate.fallback);

  try {
    return [rtDecodeOne(document, unpinned), document];
  } catch (err) {
    if (!(err instanceof UnknownMessageError) || fallbacks.length === 0) {
      throw err;
    }
  }

  return [rtDecodeOne(document, fallbacks), document];
}
