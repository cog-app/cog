// Feed player: custom YouTube-style chrome over a <video>/<audio> element.
// Reads config from #feed-player data-* attributes. Idempotent: init() can be
// called again after an HTMX swap of #feed-player (auto-advance).
(function () {
	"use strict";

	function formatTime(sec) {
		sec = Math.floor(sec || 0);
		var h = Math.floor(sec / 3600);
		var m = Math.floor((sec % 3600) / 60);
		var s = sec % 60;
		var pad = function (n) { return (n < 10 ? "0" : "") + n; };
		return h > 0 ? h + ":" + pad(m) + ":" + pad(s) : m + ":" + pad(s);
	}

	// rangeAt returns the first range containing t, or null.
	function rangeAt(t, ranges) {
		for (var i = 0; i < ranges.length; i++) {
			if (t >= ranges[i].start && t <= ranges[i].end) return ranges[i];
		}
		return null;
	}

	function post(url, body, csrf) {
		return fetch(url, {
			method: "POST",
			headers: {
				"Content-Type": "application/x-www-form-urlencoded",
				"X-CSRF-Token": csrf,
			},
			body: body,
		});
	}

	function init() {
		var root = document.getElementById("feed-player");
		if (!root || root.dataset.bound === "1") return;
		root.dataset.bound = "1";

		var el = root.querySelector("#player-el");
		if (!el) return;

		var cfg = {
			itemID: root.dataset.itemId,
			type: root.dataset.mediaType,
			sourceKind: root.dataset.sourceKind,
			resume: parseFloat(root.dataset.resume) || 0,
			csrf: root.dataset.csrf,
			title: root.dataset.title || "",
			subscription: root.dataset.subscription || "",
			artwork: root.dataset.artwork || "",
			speed: parseFloat(root.dataset.speed) || 1,
			skipBack: parseInt(root.dataset.skipBack, 10) || 15,
			skipFwd: parseInt(root.dataset.skipFwd, 10) || 30,
			volume: root.dataset.volume === "" ? 1 : parseFloat(root.dataset.volume),
			autoplayNext: root.dataset.autoplayNext === "true",
			skipSilence: root.dataset.skipSilence === "true",
		};

		// --- element refs (rendered by feedPlayer templ) ---
		var playBtn = root.querySelector("#player-play");
		var backBtn = root.querySelector("#player-back");
		var fwdBtn = root.querySelector("#player-fwd");
		var timeLabel = root.querySelector("#player-time");
		var track = root.querySelector("#player-track");
		var played = root.querySelector("#player-played");
		var buffered = root.querySelector("#player-buffered");
		var handle = root.querySelector("#player-handle");
		var markers = root.querySelector("#player-markers");
		var speedBtn = root.querySelector("#player-speed");
		var volBtn = root.querySelector("#player-mute");
		var volRange = root.querySelector("#player-volume");
		var gearBtn = root.querySelector("#player-gear");
		var gearMenu = root.querySelector("#player-gear-menu");
		var fsBtn = root.querySelector("#player-fullscreen");
		var surface = root.querySelector("#player-surface");
		var centerBtn = root.querySelector("#player-center");
		var iconPlay = root.querySelector("#icon-play");
		var iconPause = root.querySelector("#icon-pause");
		var upNext = root.querySelector("#player-upnext");

		// --- initial element state ---
		el.volume = isNaN(cfg.volume) ? 1 : cfg.volume;
		el.playbackRate = cfg.speed;
		if (volRange) volRange.value = String(el.volume);
		if (speedBtn) speedBtn.textContent = cfg.speed + "×";

		// --- source loading (YouTube resolves a stream URL) ---
		if (cfg.sourceKind === "youtube") {
			fetch("/apps/feed/api/stream-url/" + cfg.itemID, {
				headers: { "X-CSRF-Token": cfg.csrf },
			})
				.then(function (r) { return r.json(); })
				.then(function (d) {
					if (d.url) {
						el.src = d.url;
						if (cfg.resume > 0) el.currentTime = cfg.resume;
						el.play().catch(function () {});
					}
				})
				.catch(function (e) { console.error("feed: stream-url", e); });
		} else {
			el.addEventListener("loadedmetadata", function () {
				if (cfg.resume > 0) el.currentTime = cfg.resume;
				el.play().catch(function () {});
			}, { once: true });
		}

		// --- ranges (mute + skip) ---
		var ranges = [];
		fetch("/apps/feed/api/ranges/" + cfg.itemID, {
			headers: { "X-CSRF-Token": cfg.csrf },
		})
			.then(function (r) { return r.json(); })
			.then(function (d) {
				ranges = (d.ranges || []);
				renderMarkers();
			})
			.catch(function (e) { console.error("feed: ranges", e); });

		function renderMarkers() {
			if (!markers || !el.duration) return;
			markers.innerHTML = "";
			for (var i = 0; i < ranges.length; i++) {
				var r = ranges[i];
				var d = document.createElement("div");
				d.className = "feed-marker feed-marker-" + r.action;
				d.style.position = "absolute";
				d.style.top = "0";
				d.style.bottom = "0";
				d.style.left = (100 * r.start / el.duration) + "%";
				d.style.width = Math.max(0.4, 100 * (r.end - r.start) / el.duration) + "%";
				markers.appendChild(d);
			}
		}

		// --- play/pause icon + MediaSession ---
		function reflectPlaying(playing) {
			if (iconPlay) iconPlay.style.display = playing ? "none" : "";
			if (iconPause) iconPause.style.display = playing ? "" : "none";
			if (centerBtn) centerBtn.style.opacity = playing ? "0" : "1";
			if ("mediaSession" in navigator) {
				navigator.mediaSession.playbackState = playing ? "playing" : "paused";
			}
		}
		el.addEventListener("play", function () { reflectPlaying(true); });
		el.addEventListener("pause", function () { reflectPlaying(false); });

		function toggle() { if (el.paused) el.play(); else el.pause(); }
		function seekBy(sec) { el.currentTime = Math.max(0, Math.min((el.duration || 1e9), el.currentTime + sec)); }
		function setSpeed(rate) {
			rate = Math.max(0.25, Math.min(4, rate));
			el.playbackRate = rate;
			if (speedBtn) speedBtn.textContent = (Math.round(rate * 100) / 100) + "×";
			savePrefs({ speed: rate });
			updatePositionState();
		}

		// --- preferences persistence (debounced) ---
		var prefTimer = null;
		var pendingPrefs = {};
		function savePrefs(partial) {
			Object.assign(pendingPrefs, partial);
			clearTimeout(prefTimer);
			prefTimer = setTimeout(function () {
				var body = Object.keys(pendingPrefs)
					.map(function (k) {
						var form = { speed: "speed", volume: "volume", skipSilence: "skip_silence", autoplayNext: "autoplay_next" }[k] || k;
						return form + "=" + encodeURIComponent(pendingPrefs[k]);
					})
					.join("&");
				pendingPrefs = {};
				post("/apps/feed/player/prefs", body, cfg.csrf)
					.catch(function (e) { console.error("feed: prefs", e); });
			}, 400);
		}

		// --- buttons ---
		if (playBtn) playBtn.addEventListener("click", toggle);
		if (centerBtn) centerBtn.addEventListener("click", toggle);
		if (backBtn) backBtn.addEventListener("click", function () { seekBy(-cfg.skipBack); });
		if (fwdBtn) fwdBtn.addEventListener("click", function () { seekBy(cfg.skipFwd); });
		if (volBtn) volBtn.addEventListener("click", function () {
			el.muted = !el.muted;
		});
		if (volRange) volRange.addEventListener("input", function () {
			el.muted = false;
			el.volume = parseFloat(volRange.value);
			savePrefs({ volume: el.volume });
		});
		if (gearBtn && gearMenu) gearBtn.addEventListener("click", function () {
			gearMenu.style.display = gearMenu.style.display === "block" ? "none" : "block";
		});
		// gear menu speed buttons (data-speed) + toggles (data-toggle).
		if (gearMenu) gearMenu.addEventListener("click", function (e) {
			var sp = e.target.getAttribute("data-speed");
			if (sp) { setSpeed(parseFloat(sp)); return; }
			var tg = e.target.getAttribute("data-toggle");
			if (tg === "skip_silence") {
				cfg.skipSilence = !cfg.skipSilence;
				e.target.setAttribute("aria-pressed", cfg.skipSilence ? "true" : "false");
				savePrefs({ skipSilence: cfg.skipSilence });
			} else if (tg === "autoplay_next") {
				cfg.autoplayNext = !cfg.autoplayNext;
				e.target.setAttribute("aria-pressed", cfg.autoplayNext ? "true" : "false");
				savePrefs({ autoplayNext: cfg.autoplayNext });
			}
		});
		if (speedBtn) speedBtn.addEventListener("click", function () {
			var steps = [1, 1.25, 1.5, 1.75, 2, 0.75];
			var idx = steps.indexOf(Math.round(el.playbackRate * 100) / 100);
			setSpeed(steps[(idx + 1) % steps.length]);
		});
		if (fsBtn) fsBtn.addEventListener("click", function () {
			var target = surface || root;
			if (document.fullscreenElement) document.exitFullscreen();
			else if (target.requestFullscreen) target.requestFullscreen();
		});

		// --- scrubber: click + drag ---
		function seekToClientX(clientX) {
			if (!track || !el.duration) return;
			var rect = track.getBoundingClientRect();
			var frac = Math.max(0, Math.min(1, (clientX - rect.left) / rect.width));
			el.currentTime = frac * el.duration;
		}
		var dragging = false;
		if (track) {
			track.addEventListener("mousedown", function (e) { dragging = true; seekToClientX(e.clientX); });
			document.addEventListener("mousemove", function (e) { if (dragging) seekToClientX(e.clientX); });
			document.addEventListener("mouseup", function () { dragging = false; });
			// touch drag
			track.addEventListener("touchstart", function (e) { dragging = true; seekToClientX(e.touches[0].clientX); }, { passive: true });
			track.addEventListener("touchmove", function (e) { if (dragging) seekToClientX(e.touches[0].clientX); }, { passive: true });
			track.addEventListener("touchend", function () { dragging = false; });
		}

		// --- touch: tap center play/pause, double-tap thirds to seek ---
		if (surface) {
			var lastTap = 0;
			surface.addEventListener("touchend", function (e) {
				var now = Date.now();
				var rect = surface.getBoundingClientRect();
				var x = e.changedTouches[0].clientX - rect.left;
				if (now - lastTap < 300) {
					if (x < rect.width / 3) seekBy(-10);
					else if (x > rect.width * 2 / 3) seekBy(10);
					else toggle();
				} else {
					showControls();
				}
				lastTap = now;
			});
		}

		// --- progress + ranges loop ---
		var lastSaved = 0;
		var wasMuted = false;
		var savedVol = el.volume;
		el.addEventListener("timeupdate", function () {
			var t = el.currentTime;
			var dur = el.duration || 0;
			if (timeLabel) timeLabel.textContent = formatTime(t) + " / " + formatTime(dur);
			if (played && dur) played.style.width = (100 * t / dur) + "%";
			if (handle && dur) handle.style.left = (100 * t / dur) + "%";
			if (buffered && dur && el.buffered.length) {
				buffered.style.width = (100 * el.buffered.end(el.buffered.length - 1) / dur) + "%";
			}

			// ranges: mute vs skip
			var hit = rangeAt(t, ranges);
			if (hit && hit.action === "skip" && cfg.skipSilence) {
				el.currentTime = hit.end;
			} else if (hit && hit.action === "mute") {
				if (!wasMuted) { savedVol = el.volume; el.volume = 0; wasMuted = true; }
			} else if (wasMuted) {
				el.volume = savedVol; wasMuted = false;
			}

			updatePositionState();

			if (Math.abs(t - lastSaved) >= 15) {
				lastSaved = t;
				post("/apps/feed/items/" + cfg.itemID + "/progress", "progress=" + t.toFixed(1), cfg.csrf)
					.catch(function (e) { console.error("feed: progress", e); });
			}
		});
		el.addEventListener("loadedmetadata", renderMarkers);

		// --- ended: complete + auto-advance ---
		el.addEventListener("ended", function () {
			post("/apps/feed/items/" + cfg.itemID + "/complete", "", cfg.csrf)
				.then(function (r) { return r.text(); })
				.then(function (html) {
					if (cfg.autoplayNext && html && html.indexOf("feed-player") !== -1) {
						if (upNext) {
							upNext.style.display = "block";
							setTimeout(function () { swapPlayer(html); }, 1500);
						} else {
							swapPlayer(html);
						}
					}
				})
				.catch(function (e) { console.error("feed: complete", e); });
		});

		function swapPlayer(html) {
			var tmp = document.createElement("div");
			tmp.innerHTML = html;
			var next = tmp.querySelector("#feed-player");
			if (next && root.parentNode) {
				root.parentNode.replaceChild(next, root);
				init(); // bind the new player
			}
		}

		// --- controls auto-hide (video) ---
		var hideTimer = null;
		function showControls() {
			root.classList.remove("controls-hidden");
			clearTimeout(hideTimer);
			if (cfg.type === "video" && !el.paused) {
				hideTimer = setTimeout(function () { root.classList.add("controls-hidden"); }, 2500);
			}
		}
		if (surface) {
			surface.addEventListener("mousemove", showControls);
			el.addEventListener("play", showControls);
		}

		// --- keyboard ---
		function typingTarget(e) {
			var n = e.target.nodeName;
			return n === "INPUT" || n === "TEXTAREA" || e.target.isContentEditable;
		}
		document.addEventListener("keydown", function (e) {
			if (typingTarget(e)) return;
			if (!document.body.contains(root)) return; // player removed
			switch (e.key) {
				case " ": case "k": e.preventDefault(); toggle(); break;
				case "ArrowLeft": seekBy(-5); break;
				case "ArrowRight": seekBy(5); break;
				case "j": seekBy(-10); break;
				case "l": seekBy(10); break;
				case "ArrowUp": el.muted = false; el.volume = Math.min(1, el.volume + 0.05); savePrefs({ volume: el.volume }); break;
				case "ArrowDown": el.muted = false; el.volume = Math.max(0, el.volume - 0.05); savePrefs({ volume: el.volume }); break;
				case "m": el.muted = !el.muted; break;
				case "f": if (fsBtn) fsBtn.click(); break;
				case "<": case ",": setSpeed(el.playbackRate - 0.25); break;
				case ">": case ".": setSpeed(el.playbackRate + 0.25); break;
				default:
					if (e.key >= "0" && e.key <= "9" && el.duration) {
						el.currentTime = el.duration * (parseInt(e.key, 10) / 10);
					}
			}
		});

		// --- MediaSession (device OS controls) ---
		function updatePositionState() {
			if (!("mediaSession" in navigator) || !navigator.mediaSession.setPositionState) return;
			if (!el.duration || !isFinite(el.duration)) return;
			try {
				navigator.mediaSession.setPositionState({
					duration: el.duration,
					position: Math.min(el.currentTime, el.duration),
					playbackRate: el.playbackRate,
				});
			} catch (e) { /* some browsers throw on bad values */ }
		}
		if ("mediaSession" in navigator) {
			var art = cfg.artwork ? [{ src: cfg.artwork, sizes: "512x512", type: "image/png" }] : [];
			navigator.mediaSession.metadata = new MediaMetadata({
				title: cfg.title,
				artist: cfg.subscription,
				artwork: art,
			});
			var handlers = {
				play: function () { el.play(); },
				pause: function () { el.pause(); },
				seekbackward: function () { seekBy(-cfg.skipBack); },
				seekforward: function () { seekBy(cfg.skipFwd); },
				seekto: function (d) { if (d.seekTime != null) el.currentTime = d.seekTime; },
				previoustrack: function () { el.currentTime = 0; },
				nexttrack: function () {
					post("/apps/feed/items/" + cfg.itemID + "/complete", "", cfg.csrf)
						.then(function (r) { return r.text(); })
						.then(function (html) { if (html.indexOf("feed-player") !== -1) swapPlayer(html); })
						.catch(function (e) { console.error("feed: nexttrack", e); });
				},
			};
			Object.keys(handlers).forEach(function (action) {
				try { navigator.mediaSession.setActionHandler(action, handlers[action]); }
				catch (e) { /* unsupported action */ }
			});
		}
	}

	window.FeedPlayer = { init: init };

	// Initialize on load and after HTMX swaps that bring in #feed-player.
	if (document.readyState !== "loading") init();
	else document.addEventListener("DOMContentLoaded", init);
	document.body.addEventListener("htmx:afterSwap", function () { init(); });
})();
