// Navy Lily — inline Join / checkout widget.
//
// Injected at the end of every free lesson via <div id="navy-join"></div> +
// <script src="/join.js"></script>. No login required: the visitor types their
// e-mail here, picks PIX (transparent, on this page) or credit card (AbacatePay
// hosted checkout). On a confirmed PIX payment we grant access and e-mail a
// login link so they can open the paid lessons.
(function () {
  var root = document.getElementById("navy-join");

  var email = "";
  var chargeId = null;

  // Active members have already paid: hide every buy call-to-action so they
  // never see a landing page again. Covers the "Navy" header button on every
  // page (a -> /comprar); the inline ad banner is simply not rendered below.
  function hideBuyUI() {
    var links = document.querySelectorAll('a[href="/comprar"]');
    for (var i = 0; i < links.length; i++) links[i].style.display = "none";
  }

  function h(html) { root.innerHTML = html; }
  function $(id) { return document.getElementById(id); }
  function esc(s) { return String(s).replace(/[&<>"]/g, function (c) {
    return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]; }); }

  // The pool of 16:9 banner artworks. Drop the images at public/navy-ad-1.png,
  // public/navy-ad-2.png, … and list each path here. One is picked at random
  // every time a lesson loads.
  var ADS = [
    "/navy-ad-1.png",
    "/navy-ad-2.png",
    "/navy-ad-3.png",
    "/navy-ad-4.png",
    "/navy-ad-5.png",
    "/navy-ad-6.png",
    "/navy-ad-7.png"
  ];

  // Step 0: a clickable 16:9 banner image (the "Join" call to action).
  // Clicking it opens the checkout (e-mail + PIX/card) inline, on this page.
  function showIntro() {
    var src = ADS[Math.floor(Math.random() * ADS.length)];
    h(
      '<a id="nj-join" href="#" style="display:block;margin-top:2.5rem;text-decoration:none;">' +
        '<img src="' + esc(src) + '" alt="Navy Assinatura — adquirir o curso" ' +
          'style="display:block;width:100%;aspect-ratio:16/9;object-fit:cover;border:1px solid #ddd;border-radius:8px;">' +
      "</a>"
    );
    $("nj-join").addEventListener("click", function (e) { e.preventDefault(); showMethods(); });
  }

  // Step 1: e-mail + payment method.
  function showMethods() {
    h(
      '<aside style="border:1px solid #ddd;border-radius:8px;padding:1rem 1.25rem;margin-top:2.5rem;font-family:system-ui,sans-serif;">' +
        '<strong style="font-size:1.1rem;">Navy Assinatura — R$ 197/ano</strong>' +
        '<p style="margin:.6rem 0 .2rem;"><label for="nj-email">Seu e-mail</label></p>' +
        '<p style="margin:.2rem 0;"><input id="nj-email" type="email" placeholder="voce@email.com" value="' + esc(email) + '" style="width:100%;max-width:20rem;"></p>' +
        '<p id="nj-err" style="margin:.2rem 0;color:#b00;" hidden></p>' +
        '<p style="margin:.6rem 0;"><button id="nj-pix" type="button">Pagar com PIX (liberação imediata)</button></p>' +
        '<p style="margin:.6rem 0;"><button id="nj-card" type="button">Cartão de crédito (até 12x)</button></p>' +
      "</aside>"
    );
    if (email) $("nj-email").value = email;
    $("nj-pix").addEventListener("click", startPix);
    $("nj-card").addEventListener("click", startCard);
  }

  function readEmail() {
    var v = ($("nj-email").value || "").trim().toLowerCase();
    if (v.indexOf("@") < 0) {
      var e = $("nj-err");
      e.textContent = "Digite um e-mail válido.";
      e.hidden = false;
      return null;
    }
    return v;
  }

  function startCard() {
    var v = readEmail();
    if (!v) return;
    location.href = "/card/new?email=" + encodeURIComponent(v);
  }

  function startPix() {
    var v = readEmail();
    if (!v) return;
    email = v;
    h('<aside style="border:1px solid #ddd;border-radius:8px;padding:1rem 1.25rem;margin-top:2.5rem;font-family:system-ui,sans-serif;"><p>Gerando seu PIX…</p></aside>');
    var body = new URLSearchParams({ email: v });
    fetch("/pix/new", { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded" }, body: body })
      .then(function (r) { if (!r.ok) throw new Error("pix/new " + r.status); return r.json(); })
      .then(function (d) { chargeId = d.id; showPix(d); poll(); })
      .catch(function () { showError(); });
  }

  function showPix(d) {
    h(
      '<aside style="border:1px solid #ddd;border-radius:8px;padding:1rem 1.25rem;margin-top:2.5rem;font-family:system-ui,sans-serif;">' +
        "<p>Escaneie o QR Code com o app do seu banco:</p>" +
        '<img alt="QR Code PIX" width="240" height="240" style="display:block;margin:1rem 0;" src="' + esc(d.brCodeBase64) + '">' +
        "<p>Ou use o PIX copia e cola:</p>" +
        '<textarea id="nj-brcode" rows="3" readonly style="width:100%;font-family:monospace;">' + esc(d.brCode) + "</textarea>" +
        '<p><button id="nj-copy" type="button">Copiar código</button></p>' +
        '<p id="nj-status" style="color:#666;">Aguardando pagamento…</p>' +
      "</aside>"
    );
    $("nj-copy").addEventListener("click", function () {
      var t = $("nj-brcode");
      t.select();
      if (navigator.clipboard) navigator.clipboard.writeText(t.value);
    });
  }

  function poll() {
    fetch("/pix/status?id=" + encodeURIComponent(chargeId))
      .then(function (r) { return r.json(); })
      .then(function (d) {
        if (d.status === "PAID" || d.status === "APPROVED") { showPaid(d); return; }
        if (d.status === "EXPIRED" || d.status === "CANCELLED" || d.status === "FAILED") {
          var s = $("nj-status");
          if (s) s.textContent = "PIX expirado. Recarregue a página para gerar outro.";
          return;
        }
        setTimeout(poll, 4000);
      })
      .catch(function () { setTimeout(poll, 4000); });
  }

  function showPaid(d) {
    var next = d.logged_in
      ? '<p>Seu acesso foi liberado. <a href="/protected/">Ir para as aulas →</a></p>'
      : "<p>Seu acesso foi liberado. Enviamos um <strong>link de acesso</strong> para <strong>" + esc(email) +
        "</strong> — abra-o para entrar nas aulas pagas.</p>";
    h(
      '<aside style="border:1px solid #ddd;border-radius:8px;padding:1rem 1.25rem;margin-top:2.5rem;font-family:system-ui,sans-serif;">' +
        "<strong style=\"font-size:1.1rem;\">✅ Pagamento confirmado!</strong>" + next +
      "</aside>"
    );
  }

  function showError() {
    h(
      '<aside style="border:1px solid #ddd;border-radius:8px;padding:1rem 1.25rem;margin-top:2.5rem;font-family:system-ui,sans-serif;">' +
        '<p>Não foi possível gerar o PIX agora. <button id="nj-retry" type="button">Tentar de novo</button></p>' +
      "</aside>"
    );
    $("nj-retry").addEventListener("click", showMethods);
  }

  // Ask who's visiting, then render. Active members get the buy UI stripped
  // out entirely (no header button, no ad). Everyone else sees the ad/checkout
  // exactly as before; if /me fails we fall back to showing it (fail open).
  fetch("/me")
    .then(function (r) { return r.json(); })
    .then(function (d) {
      if (d && d.email) email = d.email;
      if (d && d.member) { hideBuyUI(); return; }
      if (root) showIntro();
    })
    .catch(function () { if (root) showIntro(); });
})();
