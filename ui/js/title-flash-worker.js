// Ticks for the Terminals page's flashing window title (startTitleFlash in components/terminals.js).
// Browsers slow a hidden page's own repeating timers to about once a minute, but not a worker's.
setInterval(() => postMessage(0), 1000);
