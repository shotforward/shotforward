const desktopLayoutStorageKey = 'shotforward.desktop-layout';
const desktopLayoutWidth = 1440;
const responsiveViewport = 'width=device-width, initial-scale=1.0';

export function applyViewportPreference(): boolean {
  const requested = new URLSearchParams(window.location.search).get('desktop');
  if (requested === '1' || requested === '0') {
    try {
      if (requested === '1') window.localStorage.setItem(desktopLayoutStorageKey, '1');
      else window.localStorage.removeItem(desktopLayoutStorageKey);
    } catch {
      // A privacy-restricted browser can still use the query parameter for this page load.
    }
  }

  let stored = false;
  try {
    stored = window.localStorage.getItem(desktopLayoutStorageKey) === '1';
  } catch {
    stored = false;
  }
  const desktop = requested === '1' || (requested !== '0' && stored);
  const viewport = document.querySelector<HTMLMetaElement>('meta[name="viewport"]');
  if (!viewport) return desktop;

  if (desktop) {
    const scale = Math.min(1, Math.max(0.1, window.screen.width / desktopLayoutWidth));
    viewport.content = `width=${desktopLayoutWidth}, initial-scale=${scale.toFixed(4)}, minimum-scale=0.1, maximum-scale=5, user-scalable=yes`;
  } else {
    viewport.content = responsiveViewport;
  }
  document.documentElement.dataset.layout = desktop ? 'desktop' : 'responsive';
  return desktop;
}
