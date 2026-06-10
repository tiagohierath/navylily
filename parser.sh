#!/bin/bash
# Build lesson HTML from markdown.
#   content/free/*.md -> public/*.html       (open to everyone)
#   content/paid/*.md -> protected/*.html    (gated by the auth server)
#
# Per-file convention:
#   filename (e.g. 003.md) -> lesson order
#   first "# " heading     -> title
#   IMAGES/<name>.<ext>     -> optional 16:9 lesson thumbnail (jpg/jpeg/png/webp).
#     Drop an image next to the lesson under the collection's IMAGES/ folder,
#     named after the lesson (content/free/IMAGES/003.jpg for content/free/003.md).
#     When present it's copied into the served tree (public/images or
#     protected/images), shown right under the lesson's first heading, and used
#     as the lesson's thumbnail on the home page. Entirely optional per lesson.
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

# lesson_image SRCDIR NAME -> prints the path to the lesson's thumbnail source
# (SRCDIR/IMAGES/NAME.<ext>, first match wins) or returns 1 if none exists.
lesson_image() {
  local srcdir="$1" name="$2" ext
  for ext in jpg jpeg png webp; do
    if [ -f "$srcdir/IMAGES/$name.$ext" ]; then
      printf '%s' "$srcdir/IMAGES/$name.$ext"
      return 0
    fi
  done
  return 1
}

# html_escape: stdin -> stdout with &, <, >, " escaped, for text that goes into
# raw HTML (which pandoc passes through without escaping anything itself).
html_escape() { sed 's/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g; s/"/\&quot;/g'; }

# lesson_preview FILE -> the lesson's first body paragraph (first text block
# after the "# " heading, sub-headings skipped) as escaped plain text — inline
# markdown stripped — for the landing page's article previews.
lesson_preview() {
  awk '
    !seen { if ($0 ~ /^# /) seen=1; next }
    /^[[:space:]]*$/ { if (got) exit; next }
    /^#/             { if (got) exit; next }
    { sub(/^>[[:space:]]*/, ""); sub(/^[-*][[:space:]]+/, ""); printf "%s ", $0; got=1 }
  ' "$1" \
    | sed 's/\[\([^]]*\)\]([^)]*)/\1/g; s/\*//g; s/`//g; s/[[:space:]]*$//' \
    | html_escape
}

build() {
  local srcdir="$1" outdir="$2"
  shift 2
  mkdir -p "$outdir"
  for f in "$srcdir"/*.md; do
    [ -e "$f" ] || continue
    name=$(basename "$f" .md)

    # order = numeric filename; title = first "# " heading.
    order="$name"
    title=$(grep -m1 '^# ' "$f" | sed 's/^# *//; s/[[:space:]]*$//' || true)

    meta=(--metadata "title=$title" --metadata "order=$order")

    # Previous/next lesson links within this collection, for the in-page nav the
    # template renders just above the course ad. Hrefs follow the same /NNN.html
    # (free) or /protected/NNN.html (paid) scheme as the related bullets. Only
    # numbered lessons get prev/next; placeholder names (SAMPLE.md) build fine
    # without them.
    num=0 prevpad='' nextpad=''
    if [[ "$name" =~ ^[0-9]+$ ]]; then
      num=$((10#$name)); prevpad=$(printf '%03d' $((num-1))); nextpad=$(printf '%03d' $((num+1)))
    fi
    href_prefix='/'; [ "$outdir" = protected ] && href_prefix='/protected/'

    # Optional 16:9 thumbnail: copy IMAGES/<name>.<ext> into the served tree and
    # build the <img> shown under the heading. The URL mirrors the lesson's own
    # href scheme (/images/... for free, /protected/images/... for paid) so the
    # existing static / gated handlers serve it without any extra route.
    img_html=""
    if src_img=$(lesson_image "$srcdir" "$name"); then
      ext="${src_img##*.}"
      mkdir -p "$outdir/images"
      cp "$src_img" "$outdir/images/$name.$ext"
      img_html=$'\n<img class="lesson-image" src="'"${href_prefix}images/${name}.${ext}"$'" alt="'"$title"$'">\n'
    fi

    nav=()
    [ "$num" -gt 1 ] && [ -f "$srcdir/$prevpad.md" ] && nav+=(--metadata "prevhref=${href_prefix}${prevpad}.html")
    [ -f "$srcdir/$nextpad.md" ] && nav+=(--metadata "nexthref=${href_prefix}${nextpad}.html")
    [ ${#nav[@]} -gt 0 ] && nav+=(--metadata "lessonnav=1")

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

    # Body = lines up to the last real one, with the thumbnail (if any) injected
    # right after the first "# " heading; then append the related section (still
    # inside <main>, so it lands above the course ad that the template adds after
    # the body). hdr=0 (no heading) puts the image at the very top.
    hdr=$(awk '/^# /{print NR; exit}' "$f"); [ -n "$hdr" ] || hdr=0
    { if [ "$hdr" -gt 0 ]; then sed -n "1,${hdr}p" "$f"; fi
      printf '%s' "$img_html"
      sed -n "$((hdr+1)),${keep}p" "$f"
      printf '%s' "$related"; } | pandoc \
      -f markdown+lists_without_preceding_blankline \
      --template=template.html \
      "${meta[@]}" \
      ${nav[@]+"${nav[@]}"} \
      "$@" \
      -o "${outdir}/${name}.html"
    echo "built: ${outdir}/${name}.html"
  done
}

# articles SECTION SRCDIR HREFPREFIX -> prints a "## SECTION" heading followed
# by an article list: each lesson is one row — square thumbnail on the left
# (gray placeholder when the lesson has no image; the URL mirrors the lesson
# href scheme), numbered title plus first-paragraph preview on the right.
# Emitted as raw HTML, each <li> on a single line so pandoc passes it through
# untouched. The number lives inside the title text so it stays put when the
# search filter hides items (an <ol> would renumber and leave gaps).
articles() {
  local section="$1" srcdir="$2" prefix="$3" f name title preview thumb n img ext lock
  # Paid lessons carry a small lock so the gate is no surprise on click.
  lock=''
  [ "$prefix" = "/protected" ] && lock=' 🔒'
  printf '\n## %s\n\n' "$section"
  printf '<ul class="articles">\n'
  n=0
  for f in "$srcdir"/*.md; do
    [ -e "$f" ] || continue
    n=$((n+1))
    name=$(basename "$f" .md)
    title=$(grep -m1 '^# ' "$f" | sed 's/^# *//; s/[[:space:]]*$//' | html_escape)
    preview=$(lesson_preview "$f")
    thumb='<span class="thumb"></span>'
    if img=$(lesson_image "$srcdir" "$name"); then
      ext="${img##*.}"
      thumb='<img class="thumb" src="'"$prefix/images/$name.$ext"'" alt="'"$title"'">'
    fi
    printf '<li class="article"><a href="%s/%s.html">%s<div class="text"><p class="title">%d. %s%s</p>' \
      "$prefix" "$name" "$thumb" "$n" "$title" "$lock"
    [ -n "$preview" ] && printf '<p class="preview">%s</p>' "$preview"
    printf '</div></a></li>\n'
  done
  printf '</ul>\n'
}

# build_index writes public/lessons.html: the search destination — a single page
# listing every free and paid lesson as article rows. search=1 turns on the
# header's live filter (which now matches preview text too).
build_index() {
  { printf '# Aulas\n'
    articles "Aulas gratuitas" content/free ""
    articles "Aulas pagas" content/paid "/protected"
  } | pandoc \
        -f markdown+lists_without_preceding_blankline-markdown_in_html_blocks-native_divs-native_spans \
        --template=template.html \
        --metadata "title=Aulas" --metadata "search=1" --metadata "articles=1" \
        -o public/lessons.html
  echo "built: public/lessons.html"
}

# build_root writes public/root.html: the site landing page (served at "/"). A
# plain headline + tagline, a "Ver aulas" button, then the lessons as article
# rows — thumbnail left, title + preview right (articles=1 styles them).
build_root() {
  { printf '# Aprenda a desenhar de imaginação\n\n'
    printf 'Curso gratuito, completo.\n\n'
    printf '[Ver aulas](#aulas-gratuitas){.btn}\n'
    articles "Aulas gratuitas" content/free ""
    articles "Aulas pagas" content/paid "/protected"
  } | pandoc \
        -f markdown+lists_without_preceding_blankline-markdown_in_html_blocks-native_divs-native_spans \
        --template=template.html \
        --metadata "title=Aprenda a desenhar de imaginação" --metadata "articles=1" \
        -o public/root.html
  echo "built: public/root.html"
}

# Free lessons get the subscription banner; paid lessons don't.
build content/free public -V banner=1
build content/paid protected
build_index
build_root
