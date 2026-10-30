// cog.js — behaviour for the page shell and the shared components in
// style.css. Loaded synchronously in <head> on every page, so it only defines
// functions and document-level listeners; anything that needs the DOM waits for
// DOMContentLoaded. The one exception is cog.fitNavbar(), which the layout
// calls inline right after the navbar so it never paints at the wrong stage.
(function () {
  "use strict";

  var cog = (window.cog = window.cog || {});

  // --- Navbar collapse -----------------------------------------------------
  // The nav shows as labelled icons, then labels under icons, then icons
  // alone, and finally collapses into the apps menu. Each stage is tried in
  // turn and kept as soon as the nav stops overflowing its box. Measuring the
  // real layout (rather than estimating item widths) keeps the choice the same
  // for a given width whatever the page underneath does.
  var STAGES = ["", "navbar--compact", "navbar--icon-only", "navbar--collapsed"];

  function fit(nb) {
    var nav = nb.querySelector(".navbar-nav");
    if (!nav || !nav.children.length) return;
    for (var i = 0; i < STAGES.length; i++) {
      for (var j = 1; j < STAGES.length; j++) nb.classList.remove(STAGES[j]);
      if (STAGES[i]) nb.classList.add(STAGES[i]);
      if (i === STAGES.length - 1) return;
      if (nav.scrollWidth <= nav.clientWidth + 1) return;
    }
  }

  cog.fitNavbar = function () {
    var nb = document.querySelector(".navbar");
    if (!nb) return;
    fit(nb);
    var last = nb.offsetWidth;
    var refit = function () {
      if (nb.offsetWidth === last) return;
      last = nb.offsetWidth;
      fit(nb);
    };
    if (window.ResizeObserver) new ResizeObserver(refit).observe(nb);
    else window.addEventListener("resize", refit);
  };

  // --- Dropdowns ------------------------------------------------------------
  // <div class="dropdown">
  //   <button class="dropdown-toggle" data-dropdown-toggle aria-expanded="false" aria-controls="m">
  //   <div class="dropdown-menu" id="m">…</div>
  // </div>
  function toggleOf(dd) {
    return dd.querySelector("[data-dropdown-toggle]");
  }

  function items(dd) {
    return Array.prototype.slice.call(
      dd.querySelectorAll(".dropdown-menu a, .dropdown-menu button"),
    );
  }

  function openDropdown(dd) {
    closeDropdowns(dd);
    dd.classList.add("open");
    var t = toggleOf(dd);
    if (t) t.setAttribute("aria-expanded", "true");
    dd.dispatchEvent(new CustomEvent("cog:dropdown-open", { bubbles: true }));
  }

  function closeDropdown(dd, focusToggle) {
    if (!dd.classList.contains("open")) return;
    dd.classList.remove("open");
    var t = toggleOf(dd);
    if (t) {
      t.setAttribute("aria-expanded", "false");
      if (focusToggle) t.focus();
    }
  }

  // closeDropdowns closes every open dropdown except keep.
  function closeDropdowns(keep) {
    document.querySelectorAll(".dropdown.open").forEach(function (dd) {
      if (dd !== keep) closeDropdown(dd, false);
    });
  }
  cog.closeDropdowns = closeDropdowns;

  document.addEventListener("click", function (e) {
    var t = e.target.closest("[data-dropdown-toggle]");
    if (t) {
      var dd = t.closest(".dropdown");
      if (dd.classList.contains("open")) closeDropdown(dd, false);
      else openDropdown(dd);
      return;
    }
    // A click anywhere else closes menus it is not inside.
    document.querySelectorAll(".dropdown.open").forEach(function (dd) {
      if (!dd.contains(e.target)) closeDropdown(dd, false);
    });
  });

  document.addEventListener("keydown", function (e) {
    var dd = e.target.closest && e.target.closest(".dropdown");
    if (e.key === "Escape") {
      var open = document.querySelector(".dropdown.open");
      if (open) {
        closeDropdown(open, true);
        e.preventDefault();
      }
      return;
    }
    if (!dd || (e.key !== "ArrowDown" && e.key !== "ArrowUp")) return;
    var list = items(dd);
    if (!list.length) return;
    e.preventDefault();
    if (!dd.classList.contains("open")) openDropdown(dd);
    var idx = list.indexOf(document.activeElement);
    if (e.key === "ArrowDown") idx = idx < 0 ? 0 : (idx + 1) % list.length;
    else idx = idx <= 0 ? list.length - 1 : idx - 1;
    list[idx].focus();
  });

  // Tabbing out of a menu closes it.
  document.addEventListener("focusout", function (e) {
    var dd = e.target.closest && e.target.closest(".dropdown.open");
    if (dd && e.relatedTarget && !dd.contains(e.relatedTarget)) {
      closeDropdown(dd, false);
    }
  });

  // --- Search ---------------------------------------------------------------
  function searchBox() {
    return document.getElementById("search-box");
  }

  cog.openSearch = function () {
    var nb = document.querySelector(".navbar");
    var box = searchBox();
    if (!nb || !box) return;
    closeDropdowns();
    nb.classList.add("search-open");
    var t = nb.querySelector(".search-toggle");
    if (t) t.setAttribute("aria-expanded", "true");
    box.focus();
    box.select();
  };

  cog.closeSearch = function () {
    var nb = document.querySelector(".navbar");
    if (!nb || !nb.classList.contains("search-open")) return;
    nb.classList.remove("search-open");
    var t = nb.querySelector(".search-toggle");
    if (t) t.setAttribute("aria-expanded", "false");
  };

  document.addEventListener("click", function (e) {
    if (e.target.closest("[data-search-open]")) {
      e.preventDefault();
      var nb = document.querySelector(".navbar");
      if (nb && nb.classList.contains("search-open")) cog.closeSearch();
      else cog.openSearch();
      return;
    }
    var nb2 = document.querySelector(".navbar.search-open");
    if (nb2 && !e.target.closest(".navbar-search")) cog.closeSearch();
  });

  function initSearch() {
    var box = searchBox();
    if (!box) return;
    var results = document.getElementById("search-results");

    document.addEventListener("keydown", function (e) {
      var a = document.activeElement;
      if (
        e.key === "/" &&
        !e.ctrlKey &&
        !e.metaKey &&
        !["INPUT", "TEXTAREA", "SELECT"].includes(a.tagName) &&
        !a.isContentEditable
      ) {
        e.preventDefault();
        var visible = box.offsetParent !== null;
        if (visible) {
          box.focus();
          box.select();
        } else {
          cog.openSearch();
        }
      }
    });

    box.addEventListener("keydown", function (e) {
      var hits = results.querySelectorAll(".search-hit");
      if (e.key === "Escape") {
        box.blur();
        cog.closeSearch();
        return;
      }
      if (!hits.length) return;
      var cur = results.querySelector(".search-hit-active");
      var idx = cur ? Array.prototype.indexOf.call(hits, cur) : -1;
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        if (cur) cur.classList.remove("search-hit-active");
        if (e.key === "ArrowDown") idx = (idx + 1) % hits.length;
        else idx = idx <= 0 ? hits.length - 1 : idx - 1;
        hits[idx].classList.add("search-hit-active");
        hits[idx].scrollIntoView({ block: "nearest" });
      } else if (e.key === "Enter" && cur) {
        e.preventDefault();
        cur.click();
      }
    });

    new MutationObserver(function () {
      var hits = results.querySelectorAll(".search-hit");
      hits.forEach(function (h) {
        h.classList.remove("search-hit-active");
      });
      if (hits.length) hits[0].classList.add("search-hit-active");
    }).observe(results, { childList: true });
  }

  // --- Notifications ----------------------------------------------------------
  // The bell stays hidden until there is something to show.
  function initNotifications() {
    var wrap = document.getElementById("notif");
    if (!wrap) return;
    var badge = document.getElementById("notif-badge");
    var panel = document.getElementById("notif-panel");
    var pending = [];

    function render() {
      panel.textContent = "";
      pending.forEach(function (n) {
        var a = document.createElement("a");
        a.href = "/apps/calendar/?date=" + n.start.slice(0, 10);
        var name = document.createElement("span");
        name.className = "search-hit-name";
        name.textContent = n.summary;
        var sub = document.createElement("span");
        sub.className = "search-hit-sub";
        sub.textContent = new Date(n.start).toLocaleString();
        a.appendChild(name);
        a.appendChild(sub);
        panel.appendChild(a);
      });
    }

    function check() {
      fetch("/apps/calendar/notifications")
        .then(function (r) {
          return r.ok ? r.json() : [];
        })
        .then(function (data) {
          if (!data || !data.length) return;
          pending = pending.concat(data);
          wrap.hidden = false;
          badge.textContent = pending.length;
          render();
          if (!("Notification" in window)) return;
          if (Notification.permission === "granted") {
            data.forEach(function (n) {
              new Notification("Reminder: " + n.summary, {
                body: "Starting " + new Date(n.start).toLocaleString(),
              });
            });
          } else if (Notification.permission !== "denied") {
            Notification.requestPermission();
          }
        })
        .catch(function (err) {
          console.warn("cog: notifications:", err);
        });
    }

    setInterval(check, 60000);
    setTimeout(check, 5000);
  }

  // --- Sidebar on phones ------------------------------------------------------
  // Below 768px a sidebar collapses behind a button that names the current
  // item. The button is added here so that every sidebar gets it, including
  // ones a plugin writes by hand.
  var MENU_ICON =
    '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><line x1="3" y1="6" x2="21" y2="6"/><line x1="3" y1="12" x2="21" y2="12"/><line x1="3" y1="18" x2="21" y2="18"/></svg>';
  var CHEVRON =
    '<svg class="sidebar-toggle-chevron" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polyline points="6 9 12 15 18 9"/></svg>';
  var sidebarSeq = 0;

  function sidebarLabel(sb) {
    var active = sb.querySelector(".sidebar-item.active");
    var text = active ? active.textContent.replace(/\s+/g, " ").trim() : "";
    return text || "Menu";
  }

  function initSidebars(root) {
    (root || document)
      .querySelectorAll(".sidebar-layout > .sidebar")
      .forEach(function (sb) {
        var layout = sb.parentElement;
        var btn = layout.querySelector(":scope > .sidebar-toggle");
        if (!btn) {
          if (!sb.id) sb.id = "sidebar-" + ++sidebarSeq;
          btn = document.createElement("button");
          btn.type = "button";
          btn.className = "sidebar-toggle";
          btn.setAttribute("aria-expanded", "false");
          btn.setAttribute("aria-controls", sb.id);
          btn.innerHTML =
            MENU_ICON + '<span class="sidebar-toggle-label"></span>' + CHEVRON;
          layout.insertBefore(btn, sb);
          layout.classList.add("has-sidebar-toggle");
        }
        // The label is drawn from an attribute rather than written as text, so
        // the page's text still holds the item's name once, in the sidebar.
        var label = sidebarLabel(sb);
        btn.querySelector(".sidebar-toggle-label").setAttribute("data-label", label);
        btn.setAttribute("aria-label", "Menu: " + label);
      });
  }
  cog.initSidebars = initSidebars;

  document.addEventListener("click", function (e) {
    var btn = e.target.closest(".sidebar-toggle");
    if (!btn) return;
    var layout = btn.parentElement;
    var open = layout.classList.toggle("sidebar-open");
    btn.setAttribute("aria-expanded", open ? "true" : "false");
  });

  // --- Modal ------------------------------------------------------------------
  // dialog.modal: open with [data-modal-open="id"] or cogModal.open(id|el);
  // close with [data-modal-close], Esc (native) or a click on the backdrop.
  function dialogOf(x) {
    return typeof x === "string" ? document.getElementById(x) : x;
  }
  var cogModal = (window.cogModal = {
    open: function (x) {
      var d = dialogOf(x);
      if (!d) return null;
      if (!d.open) d.showModal();
      return d;
    },
    close: function (x) {
      var d = dialogOf(x);
      if (d && d.open) d.close();
      return d;
    },
  });

  document.addEventListener("click", function (e) {
    var opener = e.target.closest("[data-modal-open]");
    if (opener) {
      e.preventDefault();
      cogModal.open(opener.getAttribute("data-modal-open"));
      return;
    }
    var closer = e.target.closest("[data-modal-close]");
    if (closer) {
      e.preventDefault();
      cogModal.close(closer.closest("dialog"));
      return;
    }
    // A click on the dialog element itself (not its content) is the backdrop.
    if (e.target.matches && e.target.matches("dialog.modal[open]")) {
      var r = e.target.getBoundingClientRect();
      var inside =
        e.clientX >= r.left &&
        e.clientX <= r.right &&
        e.clientY >= r.top &&
        e.clientY <= r.bottom;
      if (!inside) e.target.close();
    }
  });

  // --- Flash placement ----------------------------------------------------------
  // The layout renders a redirect's message at the top of <main>. On a page
  // with a sidebar it belongs above the content column, not above the sidebar.
  function placeFlash() {
    var flash = document.querySelector("main > [data-page-flash]");
    var col = document.querySelector("main .sidebar-content");
    if (flash && col) col.insertBefore(flash, col.firstChild);
  }

  document.addEventListener("DOMContentLoaded", function () {
    placeFlash();
    initSearch();
    initNotifications();
    initSidebars();
  });

  document.addEventListener("htmx:afterSettle", function (e) {
    initSidebars(e.target.closest(".sidebar-layout") ? document : e.target);
  });
})();
