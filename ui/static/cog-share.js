// <cog-share resource="calendar:calendar" resource-id="5"></cog-share>
// Renders the sharing panel inline.
class CogShare extends HTMLElement {
    connectedCallback() {
        var resource = this.getAttribute('resource');
        var resourceId = this.getAttribute('resource-id');
        if (!resource || !resourceId) return;

        var scope = 'sp-' + Math.random().toString(36).slice(2, 8);
        var el = this;
        el.innerHTML = '<div id="' + scope + '"><p style="color:var(--color-text-muted)">Loading…</p></div>';

        fetch('/api/shares/panel?type=' + encodeURIComponent(resource) + '&id=' + encodeURIComponent(resourceId) + '&scope=' + encodeURIComponent(scope))
            .then(function(r) { return r.text(); })
            .then(function(html) {
                el.innerHTML = html;
                htmx.process(el);
            });
    }
}

// <cog-share-button resource="calendar:calendar" resource-id="5">Share</cog-share-button>
// Renders a button that opens a modal with the sharing panel.
class CogShareButton extends HTMLElement {
    connectedCallback() {
        var resource = this.getAttribute('resource');
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
            content.innerHTML = '<p style="color:var(--color-text-muted)">Loading…</p>';
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
