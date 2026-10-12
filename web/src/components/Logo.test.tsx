import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { Logo } from './Logo';

describe('Logo', () => {
  it('uses the official ShotForward wordmark', () => {
    render(<Logo />);

    expect(screen.getByRole('img', { name: 'ShotForward' })).toHaveAttribute(
      'src',
      '/brand/shotforward-wordmark-nav.png',
    );
  });
});
