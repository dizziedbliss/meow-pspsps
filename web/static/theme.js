// Applies a saved light/dark choice before first paint, so there's no flash.
try {
  const t = localStorage.getItem("theme");
  if (t === "light" || t === "dark") document.documentElement.dataset.theme = t;
} catch { /* storage blocked: follow the OS setting */ }
