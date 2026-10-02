// Toasts fade out on their own; failed htmx requests surface as error toasts.
(function () {
  function toast(msg, kind) {
    const el = document.createElement("div");
    el.className = "toast " + (kind || "");
    el.setAttribute("role", "status");
    el.textContent = msg;
    document.getElementById("toasts").appendChild(el);
  }
  document.addEventListener("animationend", (e) => {
    if (e.target.classList && e.target.classList.contains("toast")) e.target.remove();
  });
  document.addEventListener("htmx:responseError", (e) => {
    const xhr = e.detail.xhr;
    toast((xhr.responseText || xhr.statusText || "Request failed").trim(), "err");
  });
  document.addEventListener("htmx:sendError", () => toast("Server unreachable", "err"));
})();
