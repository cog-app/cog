// Behaviour for ui.Picker, bound once on the document rather than per
// picker. A picker is often inserted after the page has loaded (the sharing
// panel is fetched into a dialog and set with innerHTML), and a <script>
// inserted that way never runs, so per-element setup would leave such a
// picker with results nobody can choose.

function pickerChoose(picker, opt) {
    var input = picker.querySelector('.picker-input');
    var results = picker.querySelector('.picker-results');
    var id = opt.dataset.id;
    var name = opt.dataset.name;

    if (picker.dataset.multi === 'true') {
        var chips = picker.querySelector('.picker-chips');
        var taken = Array.prototype.some.call(
            chips.querySelectorAll('input[type=hidden]'),
            function(h) { return h.value === id; }
        );
        if (!taken) {
            var chip = document.createElement('span');
            chip.className = 'picker-chip';
            chip.appendChild(document.createTextNode(name));
            var hidden = document.createElement('input');
            hidden.type = 'hidden';
            hidden.name = picker.dataset.name;
            hidden.value = id;
            chip.appendChild(hidden);
            var remove = document.createElement('button');
            remove.type = 'button';
            remove.dataset.pickerRemove = '';
            remove.setAttribute('aria-label', 'Remove ' + name);
            remove.textContent = '×';
            chip.appendChild(remove);
            chips.appendChild(chip);
        }
        input.value = '';
    } else {
        picker.querySelector('.picker-value').value = id;
        input.value = name;
    }
    results.innerHTML = '';
}

document.addEventListener('click', function(e) {
    var remove = e.target.closest('[data-picker-remove]');
    if (remove) {
        remove.parentElement.remove();
        return;
    }

    var opt = e.target.closest('.picker .picker-option');
    if (opt) {
        pickerChoose(opt.closest('.picker'), opt);
        return;
    }

    // A click anywhere else closes every open result list but the one
    // belonging to the picker that was clicked.
    var inside = e.target.closest('.picker');
    document.querySelectorAll('.picker').forEach(function(p) {
        if (p !== inside) p.querySelector('.picker-results').innerHTML = '';
    });
});

document.addEventListener('keydown', function(e) {
    var input = e.target.closest && e.target.closest('.picker-input');
    if (!input) return;
    var picker = input.closest('.picker');
    var results = picker.querySelector('.picker-results');
    var opts = results.querySelectorAll('.picker-option');
    if (!opts.length) return;

    var cur = results.querySelector('.picker-option.active');
    var idx = cur ? Array.prototype.indexOf.call(opts, cur) : -1;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        if (cur) cur.classList.remove('active');
        idx = e.key === 'ArrowDown'
            ? (idx + 1) % opts.length
            : (idx <= 0 ? opts.length - 1 : idx - 1);
        opts[idx].classList.add('active');
    } else if (e.key === 'Enter') {
        // Enter picks the highlighted option instead of submitting the form.
        e.preventDefault();
        if (cur) pickerChoose(picker, cur);
    } else if (e.key === 'Escape') {
        // Inside a dialog, the first Escape closes the list, not the dialog.
        e.preventDefault();
        results.innerHTML = '';
    }
});
