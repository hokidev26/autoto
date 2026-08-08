// Shared coercions for reading values out of API payloads.
//
// These exist because a payload field is never trusted to have the shape the code
// wants: a panel that assumes an object and gets null, or gets an array, throws
// while rendering and takes the surrounding view down with it. Coercing at the
// boundary keeps that failure out of every call site.
//
// This module was created to hold objectValue, which had twelve byte-identical
// private copies across the frontend. Anything added here must be a pure value
// coercion with no DOM or locale dependency, so every module can import it
// without pulling in a subsystem.

// An object, or an empty one. Arrays are rejected: every caller uses the result
// for property lookups, and an array would silently answer them with undefined
// rather than failing where the bad shape actually arrived.
export function objectValue(value) {
  return value && typeof value === "object" && !Array.isArray(value) ? value : {};
}
