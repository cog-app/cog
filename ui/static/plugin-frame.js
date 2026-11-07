// Frame side of a plugin page (D14): reports its height to the shell, and
// hands the shell any link or redirect that leaves the plugin, since this
// origin serves nothing else.
(function () {
  var body = document.body;
  var host = body.dataset.cogHost;
  if (!host) return;
  function post(m) { if (window.parent !== window) window.parent.postMessage(m, host); }
  if (body.dataset.cogPath !== undefined) {
    post({ cogNavigate: host + body.dataset.cogPath });
    return;
  }
  var prefix = body.dataset.cogPrefix || "/";
  if (window.ResizeObserver) {
    new ResizeObserver(function () {
      post({ cogHeight: document.documentElement.scrollHeight });
    }).observe(document.documentElement);
  }
  post({ cogHeight: document.documentElement.scrollHeight });
  document.addEventListener("click", function (e) {
    var a = e.target && e.target.closest ? e.target.closest("a[href]") : null;
    if (!a || a.target === "_blank") return;
    var u;
    try { u = new URL(a.getAttribute("href"), location.href); } catch (err) { return; }
    if (u.origin !== location.origin || u.pathname.indexOf(prefix) === 0) return;
    e.preventDefault();
    post({ cogNavigate: host + u.pathname + u.search + u.hash });
  });
})();
