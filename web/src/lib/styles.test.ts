import { describe, expect, it } from "vitest";
import { clear, follow, has, inCharacters, marked, parts, tidy, toggle, turn, within, type Style } from "./styles";

const b = (start: number, length: number): Style => ({ start, length, kind: "bold" });

describe("follow", () => {
  it("goes on within a style, not at its end nor at its start", () => {
    expect(follow("ab cd", "aXb cd", [b(0, 2)])).toEqual([b(0, 3)]);
    expect(follow("ab cd", "abX cd", [b(0, 2)])).toEqual([b(0, 2)]);
    expect(follow("ab cd", "ab Xcd", [b(3, 2)])).toEqual([b(4, 2)]);
  });
  it("moves what is after a change, shrinks what lost text", () => {
    expect(follow("hello world", "hello", [b(6, 5)])).toEqual([]);
    expect(follow("hello world", "help world", [b(0, 5), b(6, 5)])).toEqual([b(0, 4), b(5, 5)]);
  });
  it("text typed over a styled selection keeps its style", () => {
    expect(follow("abc", "x", [b(0, 3)])).toEqual([b(0, 1)]);
  });
});

describe("within and turn", () => {
  it("text put in takes a kind only within a style of it", () => {
    expect([within([b(2, 4)], 2, "bold"), within([b(2, 4)], 4, "bold"), within([b(2, 4)], 6, "bold")]).toEqual([false, true, false]);
  });
  it("on or off over a part, whatever it had", () => {
    expect(turn([b(0, 3)], 3, 5, "bold", true, 8)).toEqual([b(0, 5)]);
    expect(turn([b(0, 8)], 3, 5, "bold", false, 8)).toEqual([b(0, 3), b(5, 3)]);
    expect(turn([], 3, 5, "bold", false, 8)).toEqual([]);
  });
});

describe("toggle and clear", () => {
  it("on over a part, off where all of it has it", () => {
    let s = toggle([], 0, 5, "bold", 11);
    expect(s).toEqual([b(0, 5)]);
    s = toggle(s, 0, 5, "bold", 11);
    expect(s).toEqual([]);
  });
  it("one kind over another's part: the other as it was, on both sides", () => {
    expect(toggle([b(0, 11)], 3, 8, "italic", 11)).toEqual([b(0, 11), { start: 3, length: 5, kind: "italic" }]);
  });
  it("off in part, then on again: one style, nothing left between", () => {
    const off = toggle([b(0, 11)], 3, 8, "bold", 11);
    expect(toggle(off, 3, 8, "bold", 11)).toEqual([b(0, 11)]);
    expect(toggle([b(0, 3)], 3, 6, "bold", 11)).toEqual([b(0, 6)]);   // beside it: one with it
  });
  it("off in part: what is left on both sides stays", () => {
    expect(toggle([b(0, 11)], 3, 8, "bold", 11)).toEqual([b(0, 3), b(8, 3)]);
    expect(clear([b(0, 11), { start: 2, length: 4, kind: "italic" }], 3, 8, 11))
      .toEqual([b(0, 3), { start: 2, length: 1, kind: "italic" }, b(8, 3)].sort((x, y) => x.start - y.start));
  });
  it("within a word, from the menu", () => {
    expect(toggle([], 2, 4, "italic", 5)).toEqual([{ start: 2, length: 2, kind: "italic" }]);
  });
  it("on over a part that has it in places: all of it", () => {
    expect(toggle([b(0, 2), b(4, 2)], 0, 6, "bold", 6)).toEqual([b(0, 6)]);
  });
  it("code takes the others' place", () => {
    expect(toggle([b(0, 6)], 2, 4, "code", 6)).toEqual([b(0, 2), { start: 2, length: 2, kind: "code" }, b(4, 2)]);
  });
  it("has: all of a part", () => {
    expect(has([b(0, 3), b(3, 2)], 1, 5, "bold")).toBe(true);
    expect(has([b(0, 3)], 1, 5, "bold")).toBe(false);
  });
});

describe("marked", () => {
  const typed = (text: string, kept: number | null = null) => marked(text, [], text.length, kept);
  it("a pair at the edges of a word: styled, the marks gone", () => {
    expect(typed("say *γεια*")).toMatchObject({ text: "say γεια", styles: [b(4, 4)], caret: 8, from: 4 });
    expect(typed("_a b_")).toMatchObject({ text: "a b", styles: [{ start: 0, length: 3, kind: "italic" }] });
    expect(typed("~x~")?.styles).toEqual([{ start: 0, length: 1, kind: "strike" }]);
    expect(typed("`x`")?.styles).toEqual([{ start: 0, length: 1, kind: "code" }]);
    expect(typed("||x||")?.styles).toEqual([{ start: 0, length: 1, kind: "spoiler" }]);
    expect(typed("```a\nb```")?.styles).toEqual([{ start: 0, length: 3, kind: "code" }]);
  });
  it("not within a word, nor with a space inside the marks", () => {
    expect(typed("2*3*")).toBeNull();
    expect(typed("snake_case_")).toBeNull();
    expect(typed("* a*")).toBeNull();
    expect(typed("*a *")).toBeNull();
    expect(typed("**")).toBeNull();
    expect(typed("*_*")).toBeNull();
  });
  it("not where a word goes on after it", () => {
    expect(marked("*a*b", [], 3, null)).toBeNull();
    expect(marked("*a* b", [], 3, null)).toMatchObject({ text: "a b", caret: 1 });
  });
  it("one pair per line, not one the user turned back", () => {
    expect(typed("*a\nb*")).toBeNull();
    expect(typed("*a*", 0)).toBeNull();
  });
  it("styles after it move back with the text", () => {
    const r = marked("*a* xy", [b(4, 2)], 3, null);
    expect(r?.styles).toEqual([b(0, 1), b(2, 2)]);
  });
});

describe("inCharacters and parts", () => {
  it("counts characters, from where what is sent starts", () => {
    expect(inCharacters("  😀 hi", [b(5, 2)], 2)).toEqual([b(2, 2)]);
  });
  it("parts of the same styles", () => {
    expect(parts("abcd", tidy([b(1, 2), { start: 2, length: 2, kind: "italic" }], 4))).toEqual([
      { text: "a", kinds: [], url: undefined }, { text: "b", kinds: ["bold"], url: undefined }, { text: "c", kinds: ["bold", "italic"], url: undefined },
      { text: "d", kinds: ["italic"], url: undefined },
    ]);
  });
  it("links: each its own address, not one with the next", () => {
    const l = (start: number, url: string): Style => ({ start, length: 2, kind: "link", url });
    expect(tidy([l(0, "https://a.org"), l(2, "https://b.org")], 4)).toEqual([l(0, "https://a.org"), l(2, "https://b.org")]);
    expect(parts("abcd", [l(0, "https://a.org")])[0]).toEqual({ text: "ab", kinds: ["link"], url: "https://a.org" });
  });
});
