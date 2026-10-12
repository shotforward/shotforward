import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import './i18n';
import { App } from './app/App';
import './styles/global.css';
import { applyThemePreference } from './preferences';
import { applyViewportPreference } from './viewport';

applyThemePreference();
applyViewportPreference();

const root = document.getElementById('root');
if (!root) throw new Error('ShotForward root element was not found');

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
