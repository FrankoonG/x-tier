(function () {
  try {
    var preference = localStorage.getItem('xtier.theme') || 'system';
    var dark =
      preference === 'dark' ||
      (preference === 'system' && matchMedia('(prefers-color-scheme: dark)').matches);
    var theme = dark ? 'dark' : 'light';
    document.documentElement.setAttribute('data-theme', theme);
    document.documentElement.style.colorScheme = theme;
  } catch (error) {
    // Storage unavailable; ThemeProvider resolves the preference after mount.
  }
})();
