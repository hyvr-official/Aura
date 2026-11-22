(function (window, document) {
    var types = ['success', 'danger', 'warning', 'info', 'secondary'];

    var defaultIcons = {
        success: 'ri-checkbox-circle-fill',
        danger: 'ri-error-warning-fill',
        warning: 'ri-alert-fill',
        info: 'ri-information-fill',
        secondary: 'ri-information-fill'
    };

    function showToast(message, type, icon, options) {
        if (!message || typeof Toastify === 'undefined') {
            return null;
        }
        var kind = types.indexOf(type) === -1 ? 'info' : type;
        var settings = {
            text: '<i class="toastify-icon ' + (icon || defaultIcons[kind]) + '"></i>' + String(message),
            duration: kind === 'danger' || kind === 'warning' || String(message).length > 120 ? 8000 : 5000,
            gravity: 'bottom',
            position: 'right',
            close: true,
            stopOnFocus: true,
            escapeMarkup: false,
            className: 'toast-' + kind
        };
        if (options) {
            for (var key in options) {
                if (Object.prototype.hasOwnProperty.call(options, key)) {
                    settings[key] = options[key];
                }
            }
        }
        var toast = Toastify(settings);
        toast.showToast();
        return toast;
    }

    function showPageToasts() {
        var nodes = document.querySelectorAll('[data-toast]');
        for (var i = 0; i < nodes.length; i++) {
            var node = nodes[i];
            var text = node.textContent.trim();
            var title = node.getAttribute('data-toast-title');
            var href = node.getAttribute('data-toast-href');
            var options = {};
            if (title) {
                text = title + '\n' + text;
                options.duration = -1;
            }
            if (href) {
                options.destination = href;
            }
            showToast(text, node.getAttribute('data-toast') || 'success', node.getAttribute('data-toast-icon'), options);
            node.parentNode.removeChild(node);
        }
    }

    window.showToast = showToast;

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', showPageToasts);
    } else {
        showPageToasts();
    }
})(window, document);
