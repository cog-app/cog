// Subscription list on the feed settings page: filtering by text and type,
// selecting a folder's worth at once, and the bar acting on the selection.
// The form works without this; it only makes long lists manageable.
(() => {
  const form = document.getElementById("bulk-subscriptions");
  if (!form) return;
  const filter = document.getElementById("sub-filter");
  const empty = document.getElementById("sub-filter-empty");
  const bar = document.getElementById("bulk-bar");
  const count = document.getElementById("bulk-count");
  const folder = document.getElementById("bulk-folder");
  const newFolder = document.getElementById("bulk-new-folder");
  const groups = [...form.querySelectorAll(".feed-sub-group")];

  const rowsOf = (group) => [...group.querySelectorAll("li")];
  const boxOf = (row) => row.querySelector('input[name="id"]');

  // A row hidden by the filter keeps its selection out of the action, so
  // that what is moved is what is shown.
  function update() {
    let selected = 0;
    for (const group of groups) {
      const shown = rowsOf(group).filter((row) => !row.hidden);
      const checked = shown.filter((row) => boxOf(row).checked).length;
      for (const row of rowsOf(group)) boxOf(row).disabled = row.hidden;
      const toggle = group.querySelector("[data-group-toggle]");
      toggle.checked = shown.length > 0 && checked === shown.length;
      toggle.indeterminate = checked > 0 && checked < shown.length;
      selected += checked;
    }
    count.textContent = selected;
    bar.hidden = selected === 0;
    newFolder.hidden = folder.value !== "new";
    newFolder.required = folder.value === "new";
  }

  function applyFilter() {
    const text = filter.value.trim().toLowerCase();
    const type = form.querySelector('input[name="sub-type"]:checked').value;
    let any = false;
    for (const group of groups) {
      let shown = 0;
      for (const row of rowsOf(group)) {
        row.hidden = (type !== "" && row.dataset.type !== type) ||
          (text !== "" && !row.dataset.search.includes(text));
        if (!row.hidden) shown++;
      }
      group.hidden = shown === 0;
      any = any || shown > 0;
    }
    empty.hidden = any;
    update();
  }

  form.addEventListener("change", (e) => {
    if (e.target.matches("[data-group-toggle]")) {
      for (const row of rowsOf(e.target.closest(".feed-sub-group"))) {
        if (!row.hidden) boxOf(row).checked = e.target.checked;
      }
    }
    if (e.target.name === "sub-type") {
      applyFilter();
      return;
    }
    update();
    if (e.target === folder && folder.value === "new") newFolder.focus();
  });
  filter.addEventListener("input", applyFilter);
  filter.addEventListener("keydown", (e) => {
    if (e.key === "Enter") e.preventDefault();
  });
  applyFilter();
})();
