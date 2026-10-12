import type { Metadata } from 'next';
import './style.css';

export const metadata: Metadata = { title: 'Volume Studio', description: 'Author and publish immutable data volumes' };
export const dynamic = 'force-dynamic';
export default function Layout({ children }: Readonly<{ children: React.ReactNode }>) { return <html lang="en"><body>{children}</body></html>; }
