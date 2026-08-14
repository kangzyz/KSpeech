"""Validate a KSpeech hotwords file against a sherpa-onnx model.

sherpa-onnx drops a hotword it cannot turn into model tokens, and it only says
so in a log line nobody reads, so a broken entry looks exactly like one that
simply never triggers. This checks the rules that decide whether an entry can
work at all, against the token inventory of a real model:

* the file must be UTF-8 without a BOM: nothing strips it, so it is glued to
  the first token and costs the first entry. CRLF is tolerated — sherpa-onnx
  splits on whitespace and a CR counts as whitespace — but the shipped file is
  kept LF so the repository copy and the installed copy stay byte-identical;
* a line starting with '#' is parsed as a keyword threshold and crashes the
  native library on the float conversion, so comments are forbidden;
* CJK characters are looked up in tokens.txt one by one;
* Latin runs are BPE-encoded, and this model family's BPE vocabulary is
  uppercase-only, so lower-case terms cannot be encoded;
* digits and punctuation are neither CJK nor Latin, so they are looked up
  verbatim and are almost never in the inventory. Spell numbers out and let the
  ITN rule file rewrite them.

Usage:
    python scripts/check-hotwords.py assets/hotwords.txt <model directory>

The model directory is the one holding tokens.txt, e.g.
%APPDATA%\\KSpeech\\plugins\\<resource>\\<model>.
"""

from __future__ import annotations

import sys
import unicodedata
from pathlib import Path


def load_tokens(model_directory: Path) -> set[str]:
    tokens_path = model_directory / "tokens.txt"
    tokens: set[str] = set()
    with tokens_path.open(encoding="utf-8") as handle:
        for line in handle:
            parts = line.rstrip("\n").split(" ")
            if parts and parts[0]:
                tokens.add(parts[0])
    return tokens


def is_cjk(character: str) -> bool:
    return unicodedata.east_asian_width(character) in {"W", "F"} and character.isalpha()


def split_runs(phrase: str) -> list[tuple[str, str]]:
    """Splits a phrase into ('cjk'|'latin'|'other', text) runs the way
    sherpa-onnx's SplitUtf8/MergeCharactersIntoWords does."""
    runs: list[tuple[str, str]] = []
    for character in phrase:
        if character == " ":
            continue
        if is_cjk(character):
            kind = "cjk"
        elif character.isascii() and character.isalpha():
            kind = "latin"
        else:
            kind = "other"
        if runs and runs[-1][0] == kind == "latin":
            runs[-1] = (kind, runs[-1][1] + character)
        else:
            runs.append((kind, character))
    return runs


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__)
        return 2
    hotwords_path = Path(sys.argv[1])
    model_directory = Path(sys.argv[2])
    raw = hotwords_path.read_bytes()

    problems: list[str] = []
    notes: list[str] = []
    if raw.startswith(b"\xef\xbb\xbf"):
        problems.append("file starts with a UTF-8 BOM, which costs the first entry")
    if b"\r" in raw:
        notes.append("file uses CRLF line endings; sherpa-onnx tolerates it, the shipped copy should not")

    tokens = load_tokens(model_directory)
    has_bpe_vocabulary = (model_directory / "bpe.vocab").is_file()

    seen: dict[str, int] = {}
    entries = 0
    latin_entries = 0
    for number, raw_line in enumerate(raw.decode("utf-8").splitlines(), start=1):
        line = raw_line.strip()
        if not line:
            continue
        if line.startswith("#"):
            problems.append(f"line {number}: comments crash sherpa-onnx: {line}")
            continue
        words = [word for word in line.split() if not word.startswith(":")]
        phrase = " ".join(words)
        if not phrase:
            problems.append(f"line {number}: no phrase before the score")
            continue
        entries += 1
        if phrase in seen:
            problems.append(f"line {number}: duplicate of line {seen[phrase]}: {phrase}")
        seen.setdefault(phrase, number)

        for kind, text in split_runs(phrase):
            if kind == "cjk":
                if text not in tokens:
                    problems.append(f"line {number}: {text} is not in tokens.txt")
            elif kind == "latin":
                latin_entries += 1
                if text != text.upper():
                    problems.append(f"line {number}: Latin terms must be upper case: {text}")
                missing = [c for c in text.upper() if c not in tokens]
                if missing:
                    problems.append(f"line {number}: no token for {''.join(missing)} in {text}")
            else:
                problems.append(
                    f"line {number}: {text!r} is neither CJK nor Latin; "
                    "spell numbers out and drop punctuation"
                )

    print(f"entries: {entries}")
    print(f"model: {model_directory.name}")
    print(f"bpe.vocab: {'present' if has_bpe_vocabulary else 'missing'}")
    if latin_entries and not has_bpe_vocabulary:
        notes.append(
            f"this model has no bpe.vocab, so its {latin_entries} Latin "
            "entries will be dropped by sherpa-onnx at load time"
        )
    for note in notes:
        print(f"note: {note}")
    if problems:
        print(f"problems: {len(problems)}")
        for problem in problems:
            print(f"  {problem}")
        return 1
    print("problems: none")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
