// Host side of a plugin frame (D14): sizes the frame to its content and
// follows a navigation the frame asks for, but only to this origin and only
// from the frame's own origin.
(function () {
  var frame = document.currentScript.previousElementSibling;
  if (!frame || frame.tagName !== "IFRAME") return;
  var origin = frame.dataset.origin;
  window.addEventListener("message", function (e) {
    if (e.origin !== origin || e.source !== frame.contentWindow) return;
    var d = e.data || {};
    if (typeof d.cogHeight === "number" && d.cogHeight > 0) {
      frame.style.height = Math.max(400, Math.ceil(d.cogHeight)) + "px";
    }
    if (typeof d.cogNavigate === "string") {
      var u;
      try { u = new URL(d.cogNavigate, location.origin); } catch (err) { return; }
      if (u.origin === location.origin) location.href = u.href;
    }
  });
})();
