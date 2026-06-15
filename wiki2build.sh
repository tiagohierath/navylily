#!/usr/bin/env bash
# Build wiki2 markdown -> public/wiki/*.html with first-person cleanup,
# search tags, and practical tips. One-shot generator. Requires pandoc.
set -euo pipefail
cd "$(dirname "$0")"
SRC="content/wiki2"
OUT="public/wiki"
DATA="wiki_meta.tsv"   # slug<TAB>title<TAB>category<TAB>tags(comma)<TAB>tips(||sep)
mkdir -p "$OUT"

# ---- slugify (PT title -> slug) -------------------------------------------
slugify() {
  echo "$1" \
  | sed -e 's/[ÁÀÂÃÄáàâãä]/a/g; s/[ÉÈÊËéèêë]/e/g; s/[ÍÌÎÏíìîï]/i/g' \
        -e 's/[ÓÒÔÕÖóòôõö]/o/g; s/[ÚÙÛÜúùûü]/u/g; s/[Çç]/c/g; s/[Ññ]/n/g' \
  | tr 'A-Z' 'a-z' \
  | sed -e 's/[^a-z0-9]\+/-/g; s/^-//; s/-$//'
}

# ---- wikilink + first-person cleanup on markdown --------------------------
clean_md() {
  # $1 = markdown file
  perl -CSD -0777 -pe '
    # [[Target|Label]] and [[Target]] -> markdown links to /wiki/slug.html
    sub slug {
      my $s = shift;
      $s =~ tr/ÁÀÂÃÄáàâãä/aaaaaaaaaa/;
      $s =~ tr/ÉÈÊËéèêë/eeeeeee/;
      $s =~ tr/ÍÌÎÏíìîï/iiiiiii/;
      $s =~ tr/ÓÒÔÕÖóòôõö/oooooooooo/;
      $s =~ tr/ÚÙÛÜúùûü/uuuuuuuu/;
      $s =~ tr/Çç/cc/; $s =~ tr/Ññ/nn/;
      $s = lc $s; $s =~ s/[^a-z0-9]+/-/g; $s =~ s/^-//; $s =~ s/-$//; return $s;
    }
    s/\[\[([^\]|]+)\|([^\]]+)\]\]/"[".$2."](\/wiki\/".slug($1).".html)"/ge;
    s/\[\[([^\]]+)\]\]/"[".$1."](\/wiki\/".slug($1).".html)"/ge;
  ' "$1" \
  | sed -E \
    -e 's/^O "iceberg" do autor: os/Os/' \
    -e 's/A ideia, contraintuitiva para muitos artistas, de que/Defendo, mesmo sendo contraintuitivo, que/' \
    -e 's/A defesa do autor de que/Defendo que/' \
    -e 's/A opinião do autor sobre/O que eu penso sobre/' \
    -e 's/A visão do autor sobre/Como eu vejo/' \
    -e 's/As ideias do autor sobre/Minhas ideias sobre/' \
    -e 's/As técnicas de produtividade que o autor testou/As técnicas de produtividade que testei/' \
    -e 's/A lista de livros que o autor recomenda/A lista de livros que recomendo/' \
    -e 's/O ciclo de aprendizado que o autor usa/O ciclo de aprendizado que uso/' \
    -e 's/A rotina prática do autor para/Minha rotina prática para/' \
    -e 's/O modo de trabalho preferido do autor:/Meu modo de trabalho preferido:/' \
    -e 's/Como o autor construiu seu estilo:/Como eu construí meu estilo:/' \
    -e 's/O uso intensivo de \*\*cadernos de papel\*\* pelo autor —/Uso cadernos de papel intensamente —/' \
    -e 's/Um ensaio especulativo do autor sobre/Um ensaio especulativo meu sobre/' \
    -e 's/A "visão de mundo" mais geral do autor/Minha "visão de mundo" mais geral/' \
    -e 's/o conceito que o autor usa/o conceito que uso/' \
    -e 's/Uma reflexão do autor que/Uma reflexão minha que/' \
    -e 's/Conceito aprendido pelo autor com/Conceito que aprendi com/' \
    -e 's/Os melhores resultados do autor vieram/Meus melhores resultados vieram/' \
    -e 's/que o autor considera as/que considero as/' \
    -e 's/Para quem começa do zero, o conselho é/Para quem começa do zero, meu conselho é/' \
    -e 's/Algumas referências do autor:/Algumas referências que uso:/' \
    -e 's/o autor insiste:/eu insisto:/' \
    -e 's/o autor não é fã/eu não sou fã/' \
    -e 's/No caso do autor:/No meu caso:/' \
    -e 's/o autor manipula poucas coisas:/eu manipulo poucas coisas:/' \
    -e 's/o autor: mulheres, máquinas/eu: mulheres, máquinas/' \
    -e 's/[Pp]ara o autor,?/para mim,/g' \
    -e 's/[Ss]egundo o autor,?/na minha experiência,/g' \
    -e 's/o autor desenha de imaginação/eu desenho de imaginação/' \
    -e 's/o autor chegou a ver/cheguei a ver/' \
    -e 's/o autor pegou/eu peguei/' \
    -e 's/o autor estudou/eu estudei/' \
    -e 's/O autor estudou/Eu estudei/' \
    -e 's/o autor cortou/eu cortei/' \
    -e 's/o autor evita/eu evito/' \
    -e 's/o autor mantém/eu mantenho/' \
    -e 's/o autor recomenda/eu recomendo/' \
    -e 's/o autor usa/eu uso/g' \
    -e 's/o autor diz ter/eu tenho/' \
    -e 's/o autor pirateia/eu baixo/g' \
    -e 's/o autor sente/eu sinto/' \
    -e 's/o autor aprendeu/eu aprendi/' \
    -e 's/o autor notou/eu notei/' \
    -e 's/o autor percebeu/eu percebi/' \
    -e 's/o autor decidiu/eu decidi/' \
    -e 's/o autor chegou a/cheguei a/' \
    -e 's/o autor cometeu/eu cometi/' \
    -e 's/o autor enquadra/eu enquadro/' \
    -e 's/o autor analisa/eu analiso/' \
    -e 's/o autor argumenta que/eu defendo que/' \
    -e 's/o autor não sente culpa/eu não sinto culpa/' \
    -e 's/o autor puxa/eu puxo/' \
    -e 's/o autor conta que/eu conto que/' \
    -e 's/o autor conta/eu conto/' \
    -e 's/o autor chama de/eu chamo de/' \
    -e 's/o autor define/eu defino/' \
    -e 's/o autor pensa em/eu penso em/' \
    -e 's/o autor não revela/eu não revelo/' \
    -e 's/Estimativas dele:/Minhas estimativas:/' \
    -e 's/Metáfora dele:/Minha metáfora:/' \
    -e 's/Exemplo do autor:/Meu exemplo:/' \
    -e 's/Aviso dele:/Meu aviso:/' \
    -e 's/Disclaimer dele:/Meu disclaimer:/' \
    -e 's/A lógica:/A lógica é simples:/' \
    -e 's/ \(o autor cita Evangelion/ (eu cito Evangelion/' \
    -e 's/he atingiu 99%/Eu atingi 99%/' \
    -e 's/Ele atingiu 99%/Eu atingi 99%/' \
    -e 's/Ele a descreve como/Eu a descrevo como/' \
    -e 's/Ele defende fortemente/Eu defendo fortemente/' \
    -e 's/ e costuma usar PDFs\./ e costumo usar PDFs./' \
    -e 's/Hoje o autor/Hoje eu/' \
    -e 's/A grande tese do autor:/Minha grande tese:/' \
    -e 's/segundo o autor, "a coisa/que, para mim, é "a coisa/' \
    -e 's/, segundo o autor,/, para mim,/g' \
    -e 's/A mesa dele é só cadernos/Minha mesa é só cadernos/' \
    -e 's/Por isso o autor mantém/Por isso mantenho/' \
    -e 's/Ele escreve os roteiros/Escrevo os roteiros/' \
    -e 's/o autor com o canal Desenho Mestre/eu, no canal Desenho Mestre/'
}

emit() {
  local slug="$1" title="$2" category="$3" tags="$4" tipsraw="$5" body="$6"
  local desc; desc=$(echo "$title" | sed 's/"/\&quot;/g')
  # tags -> hidden synonym text
  local tagtext; tagtext=$(echo "$tags" | sed 's/,/ · /g')
  # tips -> <li> list
  local tipshtml=""
  local IFS='|'
  read -ra arr <<< "$tipsraw"
  for t in "${arr[@]}"; do
    [ -n "$t" ] && tipshtml="$tipshtml    <li>$t</li>"$'\n'
  done
  cat > "$OUT/$slug.html" <<HTML
<!DOCTYPE html>
<html lang="pt-BR">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>$title — Navy Lily</title>
  <meta name="description" content="$desc — wiki de desenho da Navy Lily.">
  <meta name="keywords" content="$tags">
  <meta name="robots" content="index, follow">
  <link rel="canonical" href="https://tiagohierath.com/wiki/$slug.html">
  <link rel="icon" type="image/png" sizes="32x32" href="/favicon.png">
  <style>
body { font-family: serif; margin: 0; }
header.site { display: flex; align-items: center; gap: 1rem; flex-wrap: wrap; padding: .75rem 1rem; background: #ADB6C6; }
header.site .brand { font-weight: 600; text-decoration: none; color: inherit; margin-right: auto; }
header.site form { display: flex; gap: .25rem; }
header.site input[name=q] { display: none; font: inherit; padding: .25rem .5rem; }
@media (min-width: 640px) { header.site input[name=q] { display: block; } }
header.site .btn { font: inherit; text-decoration: none; color: inherit; padding: .25rem .6rem; }
main { max-width: 40rem; margin: 0 auto; padding: 1rem; line-height: 1.6; }
main h1 { line-height: 1.25; margin-bottom: .5rem; }
main h2 { line-height: 1.3; margin: 1.8rem 0 .6rem; }
main h3 { line-height: 1.3; margin: 1.6rem 0 .5rem; }
main p { margin: .85rem 0; }
main li { margin: .3rem 0; }
main blockquote { margin: 1rem 0; padding: .25rem 0 .25rem 1rem; border-left: 3px solid #ADB6C6; color: #333; font-style: italic; }
.cat { color: #666; font-size: .9rem; margin: .25rem 0 1rem; text-transform: uppercase; letter-spacing: .04em; }
.tips { margin: 2rem 0 0; padding: 1rem 1.1rem; background: #F2F5F9; }
.tips h2 { margin-top: 0; }
.backwiki { display: inline-block; margin: 1.5rem 0 0; text-decoration: none; font-weight: 600; color: #1A3F7A; }
button { background: #0B1F3A !important; color: #FFFFFF !important; }
button[type=button] { background: #28456B !important; }
input, select, textarea { background: #D7E0EC !important; color: #0B1F3A !important; accent-color: #0B1F3A; }
input::placeholder, textarea::placeholder { color: #51719A !important; }
@media (max-width: 640px) {
  header.site { gap: .4rem; padding: .5rem .6rem; }
  header.site .brand img { height: 16px !important; }
  header.site .btn { padding: .2rem .4rem; font-size: .85em; }
  header.site form button { background: none !important; border: none; padding: .2rem .4rem; }
  .lbl-rest { display: none; }
}
  </style>
</head>
<body bgcolor="white">
  <header class="site">
    <a class="btn" href="/login" aria-label="Perfil" title="Perfil">👤</a>
    <a class="brand" href="/"><img src="/logo.png?v=3" alt="Navy Lily" loading="lazy" style="height:28px;width:auto;display:block"></a>
    <a class="btn" href="/community" aria-label="Comunidade" title="Comunidade">💬<span class="lbl-rest"> Comunidade</span></a><span class="nav-sep"> · </span>
    <a class="btn" href="/wiki" aria-label="Wiki" title="Wiki">📖<span class="lbl-rest"> Wiki</span></a><span class="nav-sep"> · </span>
    <form action="/lessons.html" method="get" role="search">
      <input type="search" name="q" placeholder="Buscar aulas" aria-label="Buscar aulas">
      <button class="btn" type="submit" aria-label="Buscar" title="Buscar">🔍</button>
    </form><span class="nav-sep"> · </span>
    <a class="btn" href="/comprar" aria-label="Navy" title="Navy">⛵</a>
  </header>
  <main>
    <h1>$title</h1>
    <p class="cat">$category</p>
$body
    <div class="tips">
      <h2>Como aplicar — exercícios práticos</h2>
      <ul>
$tipshtml    </ul>
    </div>
    <a class="backwiki" href="/wiki">← Voltar à wiki</a>
    <div style="display:none" aria-hidden="true">Tags de busca: $tagtext</div>
  </main>
  <script src="/header.js?v=11" defer></script>
</body>
</html>
HTML
  echo "built: $OUT/$slug.html"
}

# ---- main loop: read DATA, build each -------------------------------------
while IFS=$'\t' read -r mdname title category tags tips; do
  [ -z "${mdname:-}" ] && continue
  case "$mdname" in \#*) continue;; esac
  slug=$(slugify "$title")
  body=$(clean_md "$SRC/$mdname" | pandoc -f markdown -t html --wrap=none)
  emit "$slug" "$title" "$category" "$tags" "$tips" "$body"
done < "$DATA"
