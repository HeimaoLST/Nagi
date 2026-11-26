#!/usr/bin/env python3
"""Browserless CI guard for Nagi repository governance files, links, budgets, and lessons."""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

AGENTS_WARN_BYTES = 12_288
AGENTS_MAX_BYTES = 24_000

PLACEHOLDER_RE = re.compile(r"\{\{[A-Z][A-Z0-9_]+\}\}")
MODULE_FENCE_RE = re.compile(r"<!--\s*(?:OPTIONAL MODULE|END MODULE):")
LINK_RE = re.compile(r"(?<!!)\[[^\]]+\]\(([^)]+)\)")
LINE_NUM_LINK_RE = re.compile(r"#L\d+(?:-L?\d+)?$")
DATE_HEADING_RE = re.compile(r"^#{2,3}\s+\d{4}-\d{2}-\d{2}\b", re.MULTILINE)
FENCED_BLOCK_RE = re.compile(r"^```.*?^```", re.MULTILINE | re.DOTALL)
INLINE_CODE_RE = re.compile(r"`[^`\n]+`")

REQUIRED_LESSON_FIELDS = (
    "Problem",
    "Cause or correction",
    "Prevention",
)

REQUIRED_PR_SECTIONS = (
    "Semantic Change Description",
    "Fail-Before-Fix",
    "Verification Evidence",
    "UI Review",
)


def strip_code(text: str) -> str:
    without_fences = FENCED_BLOCK_RE.sub("", text)
    return INLINE_CODE_RE.sub("", without_fences)


def find_first_existing(repo: Path, candidates: tuple[str, ...]) -> Path | None:
    for rel in candidates:
        path = repo / rel
        if path.is_file():
            return path
    return None


def collect_governance_docs(repo: Path, lessons_path: Path | None, pr_template: Path | None) -> list[Path]:
    docs: list[Path] = []
    agents_path = repo / "AGENTS.md"
    if agents_path.is_file():
        docs.append(agents_path)
    if lessons_path is not None:
        docs.append(lessons_path)
    if pr_template is not None:
        docs.append(pr_template)
    docs_dir = repo / "docs"
    if docs_dir.is_dir():
        docs.extend(sorted(docs_dir.rglob("*.md")))
    return docs


def check_agents_budget(repo: Path, errors: list[str], warnings: list[str]) -> None:
    agents = repo / "AGENTS.md"
    if not agents.is_file():
        errors.append("Missing required root file: AGENTS.md")
        return
    size = agents.stat().st_size
    if size == 0:
        errors.append("AGENTS.md is empty")
    elif size > AGENTS_MAX_BYTES:
        errors.append(
            f"AGENTS.md exceeds {AGENTS_MAX_BYTES} byte rule cap ({size} bytes); "
            "move details into docs/ and link them."
        )
    elif size > AGENTS_WARN_BYTES:
        warnings.append(
            f"AGENTS.md is {size} bytes (recommended <= {AGENTS_WARN_BYTES} bytes for high adherence)."
        )


def check_required_files(repo: Path, strict: bool, errors: list[str]) -> tuple[Path | None, Path | None]:
    lessons = find_first_existing(repo, ("lessons.md", "LESSONS.md"))
    if lessons is None:
        errors.append("Missing required lessons file: lessons.md (or LESSONS.md)")

    code_standards = repo / "docs" / "code-standards.md"
    if not code_standards.is_file():
        errors.append("Missing required standards file: docs/code-standards.md")

    pr_template = find_first_existing(
        repo,
        (".github/pull_request_template.md", ".github/PULL_REQUEST_TEMPLATE.md"),
    )
    if pr_template is None:
        errors.append("Missing required PR template: .github/pull_request_template.md")

    if strict:
        subsystems = repo / "docs" / "subsystems" / "README.md"
        if not subsystems.is_file():
            errors.append("Missing required subsystem index (--strict): docs/subsystems/README.md")

    return lessons, pr_template


def check_placeholders_and_links(repo: Path, docs: list[Path], errors: list[str]) -> None:
    docs_root = (repo / "docs").resolve()
    for doc in docs:
        rel_doc = doc.relative_to(repo)
        raw = doc.read_text(encoding="utf-8")
        cleaned = strip_code(raw)

        for match in PLACEHOLDER_RE.findall(cleaned):
            errors.append(f"{rel_doc}: unresolved template placeholder {match}")

        if MODULE_FENCE_RE.search(cleaned):
            errors.append(f"{rel_doc}: leftover OPTIONAL MODULE / END MODULE template fence")

        in_docs_tree = docs_root in doc.resolve().parents
        for target in LINK_RE.findall(cleaned):
            href = target.strip().strip("<>")
            if not href or href.startswith(("#", "http://", "https://", "mailto:", "file://")):
                continue

            if in_docs_tree and LINE_NUM_LINK_RE.search(href):
                errors.append(
                    f"{rel_doc}: fragile line-number link '{href}' "
                    "(link symbols and file paths instead of line numbers)"
                )

            path_part = href.split("#", 1)[0].split("?", 1)[0]
            if not path_part:
                continue

            resolved = (doc.parent / path_part).resolve()
            if not resolved.exists():
                errors.append(f"{rel_doc}: broken relative link '{href}'")


def check_lessons_schema(repo: Path, lessons: Path | None, errors: list[str]) -> None:
    if lessons is None:
        return
    rel = lessons.relative_to(repo)
    cleaned = strip_code(lessons.read_text(encoding="utf-8"))

    if DATE_HEADING_RE.search(cleaned):
        errors.append(f"{rel}: date-based diary heading detected; keep technical titles without date logs")

    active_marker = re.search(r"^##\s+Active Lessons\b.*$", cleaned, re.MULTILINE)
    body = cleaned[active_marker.end() :] if active_marker else cleaned

    sections = re.split(r"(?m)^###\s+", body)
    for section in sections[1:]:
        lines = section.strip().splitlines()
        if not lines:
            continue
        title = lines[0].strip()
        content = "\n".join(lines[1:])

        for field in REQUIRED_LESSON_FIELDS:
            pattern = re.compile(
                rf"^\s*(?:-\s+)?\*\*{re.escape(field)}(?::\*\*|\*\*:)\s*\S+",
                re.MULTILINE,
            )
            if not pattern.search(content):
                errors.append(f"{rel} [{title}]: missing required field '**{field}**:'")

        rec_match = re.search(
            r"^\s*(?:-\s+)?\*\*Recurrence(?::\*\*|\*\*:)\s*(\d+)\b",
            content,
            re.MULTILINE,
        )
        status_match = re.search(
            r"^\s*(?:-\s+)?\*\*Status(?::\*\*|\*\*:)\s*(.+)$",
            content,
            re.MULTILINE,
        )
        if rec_match:
            recurrence = int(rec_match.group(1))
            status = status_match.group(1).strip() if status_match else ""
            if recurrence >= 3 and not status.lower().startswith("graduated"):
                errors.append(
                    f"{rel} [{title}]: Recurrence={recurrence} (>= 3) must graduate to "
                    "docs/code-standards.md + automated guard and set Status: Graduated (...)"
                )


def check_pr_template(repo: Path, pr_template: Path | None, errors: list[str]) -> None:
    if pr_template is None:
        return
    rel = pr_template.relative_to(repo)
    content = pr_template.read_text(encoding="utf-8")
    for section in REQUIRED_PR_SECTIONS:
        if section.lower() not in content.lower():
            errors.append(f"{rel}: missing required section '{section}'")


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(
        description="Verify repository governance files, links, budgets, and lessons rules."
    )
    parser.add_argument("repo", nargs="?", default=".", help="Path to repository root")
    parser.add_argument(
        "--strict",
        action="store_true",
        help="Require strict governance artifacts such as docs/subsystems/README.md",
    )
    args = parser.parse_args(argv)

    repo = Path(args.repo).resolve()
    if not repo.is_dir():
        print(f"ERROR: repository path does not exist: {repo}", file=sys.stderr)
        return 2

    errors: list[str] = []
    warnings: list[str] = []

    check_agents_budget(repo, errors, warnings)
    lessons, pr_template = check_required_files(repo, args.strict, errors)
    docs = collect_governance_docs(repo, lessons, pr_template)
    check_placeholders_and_links(repo, docs, errors)
    check_lessons_schema(repo, lessons, errors)
    check_pr_template(repo, pr_template, errors)

    for warning in warnings:
        print(f"WARN: {warning}")

    if errors:
        for err in errors:
            print(f"FAIL: {err}", file=sys.stderr)
        print(f"Governance check failed with {len(errors)} error(s).", file=sys.stderr)
        return 1

    print(f"PASS: governance verified ({len(docs)} markdown doc(s) checked, 0 errors, {len(warnings)} warning(s)).")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
