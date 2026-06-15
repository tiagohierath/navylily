#!/bin/bash
# Build the downloadable course PDF (A4, via pandoc -> typst).
#   content/free/*.md -> public/downloads/curso-gratuito.pdf
#
# The whole course is free, so there's a single public PDF. Wiki pages are
# reference material, not lessons: the PDF appends them as a final "Navy Wiki"
# section, after the lessons.
#
# Cover: the thumbnail of the newest free lesson that has one, cropped to a
# square — so each rebuild's cover follows the latest illustrated lesson.
#
# Meant to run monthly (auth/deploy/navylily-pdf.timer). Output names are
# stable and each build replaces the previous file atomically, so the old
# month's PDF is deleted by the same step that publishes the new one.
set -euo pipefail
cd "$(dirname "$0")"

SITE=https://tiagohierath.com

# Portuguese month names for the output filename.
_month_num=$(date +%-m)
_year=$(date +%Y)
_months=('' Janeiro Fevereiro Março Abril Maio Junho Julho Agosto Setembro Outubro Novembro Dezembro)
PDF_NAME="Navylily_${_months[$_month_num]}_${_year}"

# Same trailing-block convention as parser.sh: lines at the end of a lesson
# that are just a lesson number (optionally free/-prefixed) are related-lesson
# references, not body text.
ref_re='^[[:space:]]*(free/)?[0-9][0-9]?[0-9]?[[:space:]]*$'

# Work in a temp .typ at the repo root so image paths like
# content/free/IMAGES/001.webp resolve relative to the source file.
typ=.pdfbuild.typ
trap 'rm -f "$typ"' EXIT

# lesson_title FILE -> first "# " heading text.
lesson_title() { grep -m1 '^# ' "$1" | sed 's/^# *//; s/[[:space:]]*$//'; }

# lesson_image SRCDIR NAME -> path of the lesson's thumbnail, or rc 1 (same
# convention as parser.sh: SRCDIR/IMAGES/NAME.<ext>).
lesson_image() {
  local ext
  for ext in jpg jpeg png webp; do
    [ -f "$1/IMAGES/$2.$ext" ] && { printf '%s' "$1/IMAGES/$2.$ext"; return 0; }
  done
  return 1
}

# related_title SRCDIR REF -> the referenced lesson's title. There's a single
# free course now, so the ref always resolves inside content/free.
related_title() {
  local srcdir="$1" ref="$2" padded
  ref="${ref#free/}"
  padded=$(printf '%03d' "$((10#$ref))")
  [ -f "content/free/$padded.md" ] || return 1
  lesson_title "content/free/$padded.md"
}

# emit_lesson SRCDIR FILE N SHIFT -> typst markup for one lesson on stdout:
# page break, "N. Title" heading (level 1+SHIFT), thumbnail under the heading,
# body, then the hand-picked related lessons as a plain titled list (the
# prev/next fallback the site uses makes no sense in a linear book).
emit_lesson() {
  local srcdir="$1" f="$2" n="$3" shift_by="$4"
  local name keep refs bullets ref t img

  name=$(basename "$f" .md)
  keep=$(awk -v re="$ref_re" '
    { lines[NR]=$0 }
    END {
      for (i=NR; i>=1; i--)
        if (lines[i] !~ /^[[:space:]]*$/ && lines[i] !~ re) break
      print i
    }' "$f")
  refs=$(awk -v re="$ref_re" -v keep="$keep" \
    'NR>keep && $0 ~ re { gsub(/[[:space:]]/,""); print }' "$f")

  bullets=""
  while IFS= read -r ref; do
    [ -n "$ref" ] || continue
    t=$(related_title "$srcdir" "$ref") || continue
    bullets+="- $t"$'\n'
  done <<< "$refs"

  printf '\n'

  # Body up to the refs block, lesson number folded into the heading — any
  # later "# " heading inside the body is demoted one level so it doesn't
  # masquerade as a lesson in the Sumário. [[wikilinks]] flatten to their text
  # and site-relative links become absolute so they work from a PDF reader.
  { sed -n "1,${keep}p" "$f" \
      | sed -E "s/^# /## /; 0,/^## /s//# $n. /;
                s/\[\[[^]|]*\|([^]]*)\]\]/\1/g; s/\[\[([^]]*)\]\]/\1/g;
                s|\]\(/|](${SITE}/|g"
    if [ -n "$bullets" ]; then printf '\n**Aulas relacionadas:**\n\n%s' "$bullets"; fi
  } | pandoc -f markdown+lists_without_preceding_blankline -t typst \
        --shift-heading-level-by="$shift_by" \
    | if img=$(lesson_image "$srcdir" "$name"); then
        # Inject the thumbnail after the heading's <label> line so the label
        # stays attached to the heading.
        awk -v img="$img" '
          { print }
          !done && seen && /^<[^>]*>$/ { printf "#pad(x: -1.5cm, image(\"%s\", width: 100%%))\n", img; done=1 }
          !seen && /^=/ { seen=1 }'
      else
        cat
      fi
}

# emit_collection SRCDIR SHIFT -> all lessons of a collection, in file order,
# numbered from 1 (matching the numbering on the site's lesson lists).
emit_collection() {
  local f n=0
  for f in "$1"/*.md; do
    [ -e "$f" ] || continue
    n=$((n+1))
    emit_lesson "$1" "$f" "$n" "$2"
  done
}

# emit_wiki SHIFT -> all wiki articles, in file order (alphabetical, like the
# site's /wiki index). Wiki files carry no "# " heading of their own — the
# filename is the title (parser.sh convention) — so one is synthesized at
# level 1+SHIFT. Same [[wikilink]]-flattening and link-absolutizing as lessons.
emit_wiki() {
  local shift_by="$1" f
  for f in content/WIKI/*.md; do
    [ -e "$f" ] || continue
    printf '\n'
    { printf '# %s\n\n' "$(basename "$f" .md)"
      sed -E "s/\[\[[^]|]*\|([^]]*)\]\]/\1/g; s/\[\[([^]]*)\]\]/\1/g;
              s|\]\(/|](${SITE}/|g" "$f"
    } | pandoc -f markdown+lists_without_preceding_blankline -t typst \
          --shift-heading-level-by="$shift_by"
  done
}

# emit_front SUBTITLE DEPTH -> document setup, cover page and table of
# contents. The cover art is the newest free lesson's thumbnail, cropped to a
# square (never stretched), so every rebuild gets the latest lesson's cover.
emit_front() {
  local subtitle="$1" depth="$2" cover='' f stamp
  for f in content/free/*.md; do
    [ -e "$f" ] || continue
    lesson_image content/free "$(basename "$f" .md)" >/dev/null && \
      cover=$(lesson_image content/free "$(basename "$f" .md)")
  done
  stamp=$(date +%Y-%m)
  cat <<EOF
#set page(paper: "a4", margin: (x: 1.5cm, y: 2cm), numbering: "1",
  footer: context align(center)[
    #text(10pt)[#counter(page).display("1")]
    #linebreak()
    #text(9pt, fill: gray)[tiagohierath.com]
  ])
#set text(lang: "pt", size: 21pt)
#set par(justify: true)
#page(numbering: none, footer: none, margin: 2cm)[
  #align(center)[
    #v(1cm)
    #text(28pt, weight: "bold")[Navy Lily]
    #v(4pt)
    #text(14pt)[$subtitle]
    #v(8pt)
    #text(10pt, fill: gray)[tiagohierath.com — $stamp]
    #v(12pt)
    #text(12pt)[Por favor compartilhe esse PDF no Discord ou com seus amigos.]
  ]
EOF
  if [ -n "$cover" ]; then
    # Placed, not flowed: a full-bleed 21cm square pinned to the bottom of
    # the page, touching the left, right and bottom edges.
    printf '  #place(bottom + center, dy: 2cm, image("%s", width: 21cm, height: 21cm, fit: "cover"))\n' "$cover"
  fi
  cat <<EOF
]
#counter(page).update(1)
#outline(title: [Sumário], depth: $depth)
#pagebreak()
EOF
}

# build_pdf OUT -> compiles the typst source on stdin into OUT, atomically
# replacing (= deleting) the previous build.
build_pdf() {
  mkdir -p "$(dirname "$1")"
  cat > "$typ"
  typst compile --format pdf "$typ" "$1.tmp"
  mv "$1.tmp" "$1"
  echo "built: $1"
}

# The whole course: every lesson, flat (lessons are level-1 headings), with the
# wiki as a final same-level section.
{ emit_front "Curso gratuito de desenho de imaginação" 1
  emit_collection content/free 0
  printf '\n= Navy Wiki\n'
  emit_wiki 0
} | build_pdf "public/downloads/${PDF_NAME}.pdf"
