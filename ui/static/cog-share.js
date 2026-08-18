// resolveResource returns the fully-qualified resource type.
// If the resource attribute already contains a colon it is used as-is.
// Otherwise the plugin prefix is read from the closest ancestor with
// a data-plugin attribute (set by the layout on <main>).
function resolveResource(el) {
    var raw = el.getAttribute('resource');
    if (!raw) return '';
    if (raw.indexOf(':') >= 0) return raw;
    var scope = el.closest('[data-plugin]');
    var prefix = scope ? scope.getAttribute('data-plugin') : '';
    return prefix ? prefix + ':' + raw : raw;
}

// <cog-share resource="calendar" resource-id="5"></cog-share>
// Renders the sharing panel inline.
// The plugin prefix is inferred from the nearest data-plugin ancestor,
// so plugins can write resource="calendar" instead of "calendar:calendar".
class CogShare extends HTMLElement {
    connectedCallback() {
        var resource = resolveResource(this);
        var resourceId = this.getAttribute('resource-id');
        if (!resource || !resourceId) return;

        var scope = 'sp-' + Math.random().toString(36).slice(2, 8);
        var el = this;
        el.innerHTML = '<div id="' + scope + '"><p style="color:var(--color-text-muted)">Loading\u2026</p></div>';

        fetch('/api/shares/panel?type=' + encodeURIComponent(resource) + '&id=' + encodeURIComponent(resourceId) + '&scope=' + encodeURIComponent(scope))
            .then(function(r) { return r.text(); })
            .then(function(html) {
                el.innerHTML = html;
                htmx.process(el);
            });
    }
}

// <cog-share-button resource="calendar" resource-id="5">Share</cog-share-button>
// Renders a button that opens a modal with the sharing panel.
class CogShareButton extends HTMLElement {
    connectedCallback() {
        var resource = resolveResource(this);
        var resourceId = this.getAttribute('resource-id');
        if (!resource || !resourceId) return;

        var label = this.textContent.trim() || 'Share';
        this.innerHTML = '';

        var btn = document.createElement('button');
        btn.className = this.getAttribute('btn-class') || 'btn btn-outline btn-sm';
        btn.textContent = label;
        this.appendChild(btn);

        btn.addEventListener('click', function() {
            var scope = 'sp-' + Math.random().toString(36).slice(2, 8);

            var overlay = document.createElement('div');
            overlay.className = 'modal-overlay';
            overlay.style.display = 'flex';
            overlay.addEventListener('click', function(e) {
                if (e.target === overlay) overlay.remove();
            });

            var content = document.createElement('div');
            content.className = 'modal-content';
            content.innerHTML = '<p style="color:var(--color-text-muted)">Loading\u2026</p>';
            overlay.appendChild(content);
            document.body.appendChild(overlay);

            fetch('/api/shares/panel?type=' + encodeURIComponent(resource) + '&id=' + encodeURIComponent(resourceId) + '&scope=' + encodeURIComponent(scope))
                .then(function(r) { return r.text(); })
                .then(function(html) {
                    content.innerHTML = html;
                    htmx.process(content);
                });
        });
    }
}

customElements.define('cog-share', CogShare);
customElements.define('cog-share-button', CogShareButton);

// cogCopy writes text to the clipboard and reports the outcome on the button
// that asked for it.
//
// source is either the text itself or a function returning it, which is what
// lets a caller copy something assembled at click time (the log viewer builds
// its text from the rows currently on screen) as well as a fixed string.
//
// The Clipboard API is only available in a secure context, so a deployment
// served over plain HTTP falls back to the old select-a-textarea trick rather
// than failing silently.
function cogCopy(btn, source) {
    var text = typeof source === 'function' ? source() : source;

    var done = function(ok) {
        if (!btn) return;
        var label = btn.getAttribute('data-copy-label') || btn.textContent;
        btn.setAttribute('data-copy-label', label);
        btn.textContent = ok ? 'Copied' : 'Copy failed';
        setTimeout(function() { btn.textContent = label; }, 1500);
    };

    if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(text).then(
            function() { done(true); },
            function() { done(false); }
        );
        return;
    }

    var ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    var ok = false;
    try {
        ok = document.execCommand('copy');
    } catch (e) {
        ok = false;
    }
    document.body.removeChild(ta);
    done(ok);
}
