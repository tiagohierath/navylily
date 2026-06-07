#!/bin/bash
# Build lesson HTML from markdown.
#   content/free/*.md -> public/*.html       (open to everyone)
#   content/paid/*.md -> protected/*.html    (gated by the auth server)
#
# Per-file convention:
#   filename (e.g. 003.md) -> lesson order
#   first "# " heading     -> title
#   second line            -> tags, separated by spaces
#   trailing lines that are just a lesson number (e.g. 002) -> related lessons.
#     They become an "Aulas relacionadas" bullet list (linked by title) shown
#     at the end of the page, above the course ad. Free and paid are independent
#     sequences that both start at 001, so a bare number resolves inside this
#     lesson's own collection first (a free lesson's "001" -> free 001, a paid
#     lesson's "001" -> paid 001). To point across collections, prefix it:
#     "free/001" or "paid/001".
set -euo pipefail
cd "$(dirname "$0")"

# Trailing-block pattern: a line that is only a 1-3 digit lesson number, with an
# optional free/ or paid/ collection prefix. (3-digit cap avoids years like 2024;
# refs are zero-padded to 3 digits to match the filenames.)
ref_re='^[[:space:]]*(free/|paid/)?[0-9][0-9]?[0-9]?[[:space:]]*$'

# related_bullet SRCDIR REF -> prints "- [Title](/href)" for lesson REF, or
# returns 1 if no such lesson exists. A "free/" or "paid/" prefix pins the
# collection; otherwise SRCDIR is checked first, so a bare number resolves
# inside its own collection before falling back to the other.
related_bullet() {
  local srcdir="$1" ref="$2" dirs padded d title
  case "$ref" in
    free/*) dirs="content/free"; ref="${ref#free/}" ;;
    paid/*) dirs="content/paid"; ref="${ref#paid/}" ;;
    *)      dirs="$srcdir content/free content/paid" ;;
  esac
  padded=$(printf '%03d' "$((10#$ref))")
  for d in $dirs; do
    [ -f "$d/$padded.md" ] || continue
    title=$(grep -m1 '^# ' "$d/$padded.md" | sed 's/^# *//; s/[[:space:]]*$//')
    case "$d" in
      */free) printf -- '- [%s](/%s.html)\n' "$title" "$padded" ;;
      */paid) printf -- '- [%s](/protected/%s.html)\n' "$title" "$padded" ;;
    esac
    return 0
  done
  return 1
}

build() {
  local srcdir="$1" outdir="$2"
  shift 2
  mkdir -p "$outdir"
  for f in "$srcdir"/*.md; do
    [ -e "$f" ] || continue
    name=$(basename "$f" .md)

    # order = numeric filename; title = first "# " heading; tags = line 2.
    order="$name"
    title=$(grep -m1 '^# ' "$f" | sed 's/^# *//; s/[[:space:]]*$//' || true)
    tags=$(sed -n '2p' "$f" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')

    meta=(--metadata "title=$title" --metadata "order=$order")
    [ -n "$tags" ] && meta+=(--metadata "tags=$tags")

    # Split off the trailing block of lesson-number lines: keep = index of the
    # last real body line; everything after it is related-lesson references.
    keep=$(awk -v re="$ref_re" '
      { lines[NR]=$0 }
      END {
        for (i=NR; i>=1; i--)
          if (lines[i] !~ /^[[:space:]]*$/ && lines[i] !~ re) break
        print i
      }' "$f")
    refs=$(awk -v re="$ref_re" -v keep="$keep" \
      'NR>keep && $0 ~ re { gsub(/[[:space:]]/,""); print }' "$f")

    # Turn each reference into a bullet; build the section only if any resolved.
    bullets=""
    while IFS= read -r ref; do
      [ -n "$ref" ] || continue
      b=$(related_bullet "$srcdir" "$ref") \
        || { echo "warn: lesson '$ref' referenced in $f not found" >&2; continue; }
      bullets+="$b"$'\n'
    done <<< "$refs"
    related=""
    [ -n "$bullets" ] && related=$'\n## Aulas relacionadas\n\n'"$bullets"

    # Body = lines up to the last real one, with the tags line (2) blanked so it
    # isn't rendered; then append the related section (still inside <main>, so it
    # lands above the course ad that the template adds after the body).
    { sed -n "1,${keep}p" "$f" | sed '2s/.*//'; printf '%s' "$related"; } | pandoc \
      --template=template.html \
      "${meta[@]}" \
      "$@" \
      -o "${outdir}/${name}.html"
    echo "built: ${outdir}/${name}.html"
  done
}

# index SECTION SRCDIR HREFPREFIX -> prints a "## SECTION" heading followed by a
# bullet list linking every lesson in SRCDIR (by title) to HREFPREFIX/NNN.html.
index() {
  local section="$1" srcdir="$2" prefix="$3" f name title
  printf '\n## %s\n\n' "$section"
  for f in "$srcdir"/*.md; do
    [ -e "$f" ] || continue
    name=$(basename "$f" .md)
    title=$(grep -m1 '^# ' "$f" | sed 's/^# *//; s/[[:space:]]*$//')
    printf -- '- [%s](%s/%s.html)\n' "$title" "$prefix" "$name"
  done
}

# build_index writes public/lessons.html: the search destination — a single page
# listing every free and paid lesson. search=1 turns on the header's live filter.
build_index() {
  { printf '# Aulas\n'
    index "Aulas gratuitas" content/free ""
    index "Aulas pagas" content/paid "/protected"
  } | pandoc --template=template.html \
        --metadata "title=Aulas" --metadata "search=1" \
        -o public/lessons.html
  echo "built: public/lessons.html"
}

# Free lessons get the subscription banner; paid lessons don't.
build content/free public -V banner=1
build content/paid protected
build_index
