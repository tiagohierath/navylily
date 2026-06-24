#!/bin/bash
# Build lesson HTML from markdown.
#   content/free/*.md -> public/*.html       (the whole course, open to everyone)
#   content/WIKI/*.md -> public/wiki/*.html  (free reference pages; filename =
#     title, slugified for the URL; Obsidian [[wikilinks]] resolve to wiki
#     pages, numeric [[006]] to that free lesson; /wiki is the index)
#
# Per-file convention:
#   filename (e.g. 003.md) -> lesson order
#   first "# " heading     -> title
#   IMAGES/<name>.<ext>     -> optional 16:9 lesson thumbnail (jpg/jpeg/png/webp).
#     Drop an image next to the lesson under the collection's IMAGES/ folder,
#     named after the lesson (content/free/IMAGES/003.jpg for content/free/003.md).
#     When present it's copied into the served tree (public/images), shown
#     right under the lesson's first heading, and used as the lesson's
#     thumbnail on the home page. Entirely optional per lesson.
#   trailing lines that are just a lesson number (e.g. 002) -> related lessons.
#     They become an "Aulas relacionadas" bullet list (linked by title) shown
#     at the end of the page, above the course ad. All lessons live in the one
#     free sequence, so "002" resolves to that lesson.
set -euo pipefail
cd "$(dirname "$0")"

# Absolute base for <link rel="canonical"> on the public pages. One canonical
# URL per page keeps Google from splitting signals across duplicate spellings
# (the server 301s /root.html -> / and /wiki.html -> /wiki to match).
SITE="${SITE_URL:-https://tiagohierath.com}"

# Trailing-block pattern: a line that is only a 1-3 digit lesson number, with an
# optional free/ collection prefix (kept for backwards compatibility). (3-digit
# cap avoids years like 2024; refs are zero-padded to 3 digits to match the
# filenames.)
ref_re='^[[:space:]]*(free/)?[0-9][0-9]?[0-9]?[[:space:]]*$'

# related_bullet SRCDIR PREFIX REF -> prints "- [Title](PREFIX/href)" for lesson
# REF resolved inside SRCDIR, or returns 1 if no such lesson exists. PREFIX is
# the course's URL prefix ("/" for the free course, "/protected/" for the paid
# one). A leading "free/"/"protected/" on the ref is tolerated for back-compat.
related_bullet() {
  local srcdir="$1" prefix="$2" ref="$3" padded title
  ref="${ref#free/}"; ref="${ref#protected/}"
  padded=$(printf '%03d' "$((10#$ref))")
  [ -f "$srcdir/$padded.md" ] || return 1
  title=$(grep -m1 '^# ' "$srcdir/$padded.md" | sed 's/^# *//; s/[[:space:]]*$//')
  printf -- '- [%s](%s%s.html)\n' "$title" "$prefix" "$padded"
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

# wiki_slug NAME -> url-safe slug for a wiki page: accents transliterated to
# ASCII, lowercased, runs of anything else collapsed to single hyphens
# ("Perspectiva Bizantina" -> "perspectiva-bizantina").
wiki_slug() {
  printf '%s' "$1" | iconv -f UTF-8 -t ASCII//TRANSLIT \
    | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9]+/-/g; s/^-//; s/-$//'
}

# wiki_link_expr -> a sed -E program that resolves Obsidian-style [[wikilinks]]
# into markdown links: [[Wiki Page Name]] points at the wiki page, a numeric
# [[006]] points at that free lesson (titled), and anything that resolves to
# nothing falls back to its own text — a missing target never breaks a link.
wiki_link_expr() {
  local g base slug n t expr=''
  for g in content/WIKI/*.md; do
    [ -e "$g" ] || continue
    base=$(basename "$g" .md)
    slug=$(wiki_slug "$base")
    expr+="s|\[\[$base\]\]|[$base](/wiki/$slug.html)|g;"
  done
  for g in content/free/*.md; do
    [ -e "$g" ] || continue
    n=$(basename "$g" .md)
    [[ "$n" =~ ^[0-9]+$ ]] || continue
    t=$(grep -m1 '^# ' "$g" | sed 's/^# *//; s/[[:space:]]*$//')
    [ -n "$t" ] || t="Aula $n"
    expr+="s|\[\[$n\]\]|[$t](/$n.html)|g;"
  done
  # Leftovers: keep the label of an aliased [[target|label]], else the target.
  expr+='s/\[\[[^]|]*\|([^]]*)\]\]/\1/g; s/\[\[([^]]*)\]\]/\1/g'
  printf '%s' "$expr"
}

# wiki_refs FILE -> markdown bullets linking each wiki page whose title appears
# anywhere in FILE's text, case-insensitively. Citing "Perspectiva Moderna" in
# a lesson's prose is enough — no inline link needed — for the article to be
# listed under "Navy Wiki" at the end of the page. Prints nothing when the
# lesson cites no wiki page.
wiki_refs() {
  local f="$1" g base
  for g in content/WIKI/*.md; do
    [ -e "$g" ] || continue
    base=$(basename "$g" .md)
    if grep -qiF -- "$base" "$f"; then
      printf -- '- [%s](/wiki/%s.html)\n' "$base" "$(wiki_slug "$base")"
    fi
  done
}

# build_wiki: content/WIKI/*.md -> public/wiki/<slug>.html + the /wiki index
# (public/wiki.html, served at /wiki). Wiki pages are free reference material
# for advanced students — open like the free lessons (banner included), but
# unnumbered: no prev/next, no completion, just the page. The title is the
# filename (wiki pages have no "# " heading of their own); an empty page still
# builds as a stub so links pointing at it keep working.
#
# The index is a plain list (wiki=1 styles it): one link per entry. Raw HTML,
# each <li> on a single line so pandoc passes it through untouched.
build_wiki() {
  local f base slug expr title
  [ -d content/WIKI ] || return 0
  mkdir -p public/wiki
  expr=$(wiki_link_expr)
  for f in content/WIKI/*.md; do
    [ -e "$f" ] || continue
    base=$(basename "$f" .md)
    slug=$(wiki_slug "$base")
    { grep -q '^# ' "$f" || printf '# %s\n\n' "$base"
      if [ -s "$f" ]; then sed -E "$expr" "$f"
      else printf '*Página em construção.*\n'; fi; } | pandoc \
      -f markdown+lists_without_preceding_blankline \
      --template=template.html \
      --metadata "title=$base" \
      --metadata "canonical=$SITE/wiki/$slug.html" \
      -V banner=1 \
      -o "public/wiki/$slug.html"
    echo "built: public/wiki/$slug.html"
  done
  { printf '# Wiki\n\nVerbetes de referência para quem quer ir além do curso — todos gratuitos.\n\n'
    printf '<ul class="wiki">\n'
    # Substack-derived notes are listed first (no separate heading — just above
    # the rest), then everything else, each group in filename order. The set is
    # listed one-per-line in content/WIKI/.substack.
    local manifest="content/WIKI/.substack"
    local pass
    for pass in substack other; do
      for f in content/WIKI/*.md; do
        [ -e "$f" ] || continue
        base=$(basename "$f" .md)
        if [ -f "$manifest" ] && grep -Fxq "$base" "$manifest"; then
          [ "$pass" = substack ] || continue
        else
          [ "$pass" = other ] || continue
        fi
        title=$(printf '%s' "$base" | html_escape)
        printf '<li><a href="/wiki/%s.html">%s</a></li>\n' \
          "$(wiki_slug "$base")" "$title"
      done
    done
    printf '</ul>\n'
    printf '<p><button onclick="var links=document.querySelectorAll(&#39;.wiki a&#39;);links[Math.floor(Math.random()*links.length)].click()">&#127922; Artigo aleatório</button></p>\n'
  } | pandoc \
    -f markdown+lists_without_preceding_blankline-markdown_in_html_blocks-native_divs-native_spans \
    --template=template.html \
    --metadata "title=Wiki" --metadata "wiki=1" \
    --metadata "canonical=$SITE/wiki" \
    -o public/wiki.html
  echo "built: public/wiki.html"
}

# html_escape: stdin -> stdout with &, <, >, " escaped, for text that goes into
# raw HTML (which pandoc passes through without escaping anything itself).
html_escape() { sed 's/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g; s/"/\&quot;/g'; }

# lesson_preview_raw FILE -> the lesson's first body paragraph (first text
# block after the "# " heading, sub-headings skipped) as plain text — inline
# markdown stripped. lesson_preview is the HTML-escaped flavor the article
# rows use; the raw flavor feeds the search index.
lesson_preview_raw() {
  awk '
    !seen { if ($0 ~ /^# /) seen=1; next }
    /^[[:space:]]*$/ { if (got) exit; next }
    /^#/             { if (got) exit; next }
    { sub(/^>[[:space:]]*/, ""); sub(/^[-*][[:space:]]+/, ""); printf "%s ", $0; got=1 }
  ' "$1" \
    | sed 's/\[\([^]]*\)\]([^)]*)/\1/g; s/\*//g; s/`//g; s/[[:space:]]*$//'
}
lesson_preview() { lesson_preview_raw "$1" | html_escape; }

build() {
  local srcdir="$1" outdir="$2" href_prefix="$3"
  shift 3
  mkdir -p "$outdir"
  for f in "$srcdir"/*.md; do
    [ -e "$f" ] || continue
    name=$(basename "$f" .md)

    # order = numeric filename; title = first "# " heading.
    order="$name"
    title=$(grep -m1 '^# ' "$f" | sed 's/^# *//; s/[[:space:]]*$//' || true)

    meta=(--metadata "title=$title" --metadata "order=$order")
    meta+=(--metadata "canonical=$SITE/$name.html")

    # Previous/next lesson links within this collection, for the in-page nav the
    # template renders just above the course ad. Hrefs are /NNN.html, the same
    # scheme as the related bullets. Only numbered lessons get prev/next;
    # placeholder names (SAMPLE.md) build fine without them.
    num=0 prevpad='' nextpad=''
    if [[ "$name" =~ ^[0-9]+$ ]]; then
      num=$((10#$name)); prevpad=$(printf '%03d' $((num-1))); nextpad=$(printf '%03d' $((num+1)))
    fi

    # Optional 16:9 thumbnail: copy IMAGES/<name>.<ext> into the served tree and
    # build the <img> shown under the heading. The URL is /images/<name>.<ext>,
    # served by the existing static handler without any extra route.
    img_html=""
    if src_img=$(lesson_image "$srcdir" "$name"); then
      ext="${src_img##*.}"
      mkdir -p "$outdir/images"
      cp "$src_img" "$outdir/images/$name.$ext"
      img_html=$'\n<img class="lesson-image" src="'"${href_prefix}images/${name}.${ext}"$'" alt="'"$title"$'" loading="lazy">\n'
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

    # No hand-picked related lessons? Fall back to the numeric neighbours that
    # exist, so every lesson still ends with somewhere to go next (titled links,
    # unlike the bare prev/next arrows in the nav).
    if [ -z "$refs" ] && [ "$num" -gt 0 ]; then
      [ -f "$srcdir/$prevpad.md" ] && refs+="$prevpad"$'\n'
      [ -f "$srcdir/$nextpad.md" ] && refs+="$nextpad"$'\n'
    fi

    # Turn each reference into a bullet; build the section only if any resolved.
    bullets=""
    while IFS= read -r ref; do
      [ -n "$ref" ] || continue
      b=$(related_bullet "$srcdir" "$href_prefix" "$ref") \
        || { echo "warn: lesson '$ref' referenced in $f not found" >&2; continue; }
      bullets+="$b"$'\n'
    done <<< "$refs"
    # Both end-of-lesson sections are collapsed <details> toggles (styled low
    # contrast by details.more in the template): present for whoever wants to
    # go deeper, quiet next to the prev/next buttons, which stay the page's
    # main call to action. The blank line before <details> matters — without
    # it pandoc glues the block onto the lesson's last paragraph.
    related=""
    [ -n "$bullets" ] && related=$'\n\n<details class="more">\n<summary>Aulas relacionadas</summary>\n\n'"$bullets"$'\n</details>\n'

    # Wiki pages cited anywhere in the lesson (title mentioned in the text,
    # case-insensitive) are listed in their own toggle right below the
    # related lessons — the prose itself stays link-free.
    wikis=$(wiki_refs "$f")
    wikirefs=""
    [ -n "$wikis" ] && wikirefs=$'\n\n<details class="more">\n<summary>Navy Wiki: Artigos avançados &amp; opcionais:</summary>\n\n'"$wikis"$'\n\n</details>\n'

    # Body = lines up to the last real one, with the thumbnail (if any) injected
    # right after the first "# " heading; then append the related + wiki
    # sections (still inside <main>, so they land above the course ad that the
    # template adds after the body). hdr=0 (no heading) puts the image at the
    # very top.
    hdr=$(awk '/^# /{print NR; exit}' "$f"); [ -n "$hdr" ] || hdr=0
    { if [ "$hdr" -gt 0 ]; then sed -n "1,${hdr}p" "$f"; fi
      printf '%s' "$img_html"
      sed -n "$((hdr+1)),${keep}p" "$f"
      printf '%s' "$related"
      printf '%s' "$wikirefs"; } | pandoc \
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
  local section="$1" srcdir="$2" prefix="$3" f name title preview thumb n img ext
  [ -n "$section" ] && printf '\n## %s\n\n' "$section"
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
      thumb='<img class="thumb" src="'"$prefix/images/$name.$ext"'" alt="'"$title"'" loading="lazy">'
    fi
    printf '<li class="article"><a href="%s/%s.html">%s<div class="text"><p class="title">%d. %s</p>' \
      "$prefix" "$name" "$thumb" "$n" "$title"
    [ -n "$preview" ] && printf '<p class="preview">%s</p>' "$preview"
    printf '</div></a></li>\n'
  done
  printf '</ul>\n'
}

# build_index writes public/lessons.html: the search destination — a single page
# listing every free and paid lesson as article rows. search=1 turns on the
# header's live filter (which now matches preview text too).
build_index() {
  { printf '<div id="mobile-search-block"><p>Pesquise qualquer aula, postagem na comunidade ou artigo na Wiki.</p><form id="mobile-search-form" action="/lessons.html" method="get" role="search"><input id="mobile-q" type="search" name="q" placeholder="Buscar..." aria-label="Buscar"></form></div>\n'
    articles "" content/free ""
  } | pandoc \
        -f markdown+lists_without_preceding_blankline-markdown_in_html_blocks-native_divs-native_spans \
        --template=template.html \
        --metadata "title=Aulas" --metadata "search=1" --metadata "articles=1" \
        --metadata "canonical=$SITE/lessons.html" \
        -o public/lessons.html
  echo "built: public/lessons.html"
}

# course_cover SLUG -> path to the course's A4 cover (content/covers/SLUG.<ext>,
# first match wins) or returns 1 if none exists. Covers are committed public
# marketing art, kept out of content/paid so the paid course's cover ships in
# the open repo even though its lessons don't.
course_cover() {
  local slug="$1" ext
  for ext in jpg jpeg png webp; do
    if [ -f "content/covers/$slug.$ext" ]; then
      printf '%s' "content/covers/$slug.$ext"
      return 0
    fi
  done
  return 1
}

# copy_cover SLUG -> copies the course cover into the served tree
# (public/covers/SLUG.<ext>) and prints its URL, or returns 1 if SLUG has no
# cover. Same content/ -> public/ copy as the lesson thumbnails.
copy_cover() {
  local slug="$1" src ext
  src=$(course_cover "$slug") || return 1
  ext="${src##*.}"
  mkdir -p public/covers
  cp "$src" "public/covers/$slug.$ext"
  printf '/covers/%s.%s' "$slug" "$ext"
}

# course_lessons SRCDIR PREFIX -> a numbered, clickable lesson list (wiki-style,
# but numbered): one <li> per lesson in SRCDIR, "<n> Title" linking to
# PREFIX<name>.html. The number is the lesson's position in the course. Raw
# HTML, one <li> per line so pandoc passes it through untouched.
course_lessons() {
  local srcdir="$1" prefix="$2" f name title n=0
  printf '<ul class="lessons">\n'
  for f in "$srcdir"/*.md; do
    [ -e "$f" ] || continue
    n=$((n+1))
    name=$(basename "$f" .md)
    title=$(grep -m1 '^# ' "$f" | sed 's/^# *//; s/[[:space:]]*$//' | html_escape)
    printf '<li><a href="%s%s.html"><span class="n">%d</span><span class="t">%s</span></a></li>\n' \
      "$prefix" "$name" "$n" "$title"
  done
  printf '</ul>\n'
}

# build_course SLUG TITLE SRCDIR PREFIX [pandoc args...] -> writes
# public/SLUG.html: the course page reached from a cover on the landing page. It
# shows the A4 cover as a hero (the course name lives in the art — no text
# heading, Netflix-style) above the numbered lesson list. PREFIX is the course's
# URL prefix ("/" free, "/protected/" paid).
build_course() {
  local slug="$1" title="$2" srcdir="$3" prefix="$4"; shift 4
  local cover hero=''
  if cover=$(copy_cover "$slug"); then
    hero='<p class="course-hero"><img src="'"$cover"'" alt="'"$title"'" width="1131" height="1599"></p>'$'\n'
  fi
  { printf '%s' "$hero"
    printf '<h1 class="visually-hidden">%s</h1>\n' "$title"
    course_lessons "$srcdir" "$prefix"
  } | pandoc \
        -f markdown+lists_without_preceding_blankline-markdown_in_html_blocks-native_divs-native_spans \
        --template=template.html \
        --metadata "title=$title" \
        --metadata "canonical=$SITE/$slug.html" \
        "$@" \
        -o "public/$slug.html"
  echo "built: public/$slug.html"
}

# build_root writes public/root.html: the site landing page (served at "/").
# Netflix-style — just the course covers (A4, the name baked into the art, so no
# text labels). Each cover links to its course page (build_course). Two courses:
# the free "Desenho do zero" and the paid "Art Sovereignty".
build_root() {
  local free_cover paid_cover
  free_cover=$(copy_cover desenho-do-zero) || free_cover='/covers/desenho-do-zero.jpg'
  paid_cover=$(copy_cover art-sovereignty) || paid_cover='/covers/art-sovereignty.jpg'
  { printf '<ul class="courses">\n'
    printf '<li><a class="course" href="/desenho-do-zero.html" aria-label="Desenho do zero"><img class="course-cover" src="%s" alt="Desenho do zero" width="1131" height="1599"></a></li>\n' "$free_cover"
    printf '<li><a class="course" href="/art-sovereignty.html" aria-label="Art Sovereignty"><img class="course-cover" src="%s" alt="Art Sovereignty" width="1131" height="1599"></a></li>\n' "$paid_cover"
    printf '</ul>\n'
  } | pandoc \
        -f markdown+lists_without_preceding_blankline-markdown_in_html_blocks-native_divs-native_spans \
        --template=template.html \
        --metadata "title=Aprenda a desenhar de imaginação" \
        --metadata "canonical=$SITE/" \
        -o public/root.html
  echo "built: public/root.html"
}

# build_search writes the client-side full-text index for the lessons page:
# one line per lesson — href, title, text — tab-separated (tabs/newlines inside
# the text are squashed to spaces, so the format needs no escaping). The whole
# course is free now, so every lesson and every wiki page contributes its full
# text to public/search.txt.
build_search() {
  local f name title text
  { for f in content/free/*.md; do
      [ -e "$f" ] || continue
      name=$(basename "$f" .md)
      title=$(grep -m1 '^# ' "$f" | sed 's/^# *//; s/[[:space:]]*$//' || true)
      [ -n "$title" ] || continue
      text=$(pandoc -t plain "$f" | tr '\n\t' '  ' | tr -s ' ')
      printf '/%s.html\t%s\t%s\n' "$name" "$title" "$text"
    done
    # Wiki pages are free: full text, brackets stripped off the wikilinks.
    for f in content/WIKI/*.md; do
      [ -e "$f" ] || continue
      name=$(basename "$f" .md)
      text=$(sed 's/\[\[//g; s/\]\]//g' "$f" | pandoc -t plain | tr '\n\t' '  ' | tr -s ' ')
      printf '/wiki/%s.html\t%s\t%s\n' "$(wiki_slug "$name")" "$name" "$text"
    done; } > public/search.txt
  echo "built: public/search.txt"
}

# Free course -> public/ (open, served at /, community banner). Paid course ->
# protected/ (gated by the auth server at /protected/, no banner).
build content/free public / -V banner=1
build content/paid protected /protected/
build_wiki
build_index
build_root
build_course desenho-do-zero "Desenho do zero" content/free / -V banner=1
build_course art-sovereignty "Art Sovereignty" content/paid /protected/
build_search
