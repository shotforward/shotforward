import '@testing-library/jest-dom/vitest';
import { configure } from '@testing-library/dom';
import { cleanup } from '@testing-library/react';
import { afterEach } from 'vitest';

// Slow CI DOM rendering must not exhaust the default one-second query window.
configure({ asyncUtilTimeout: 5_000 });

afterEach(() => cleanup());
