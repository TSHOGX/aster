import { LibraryEditor } from '../../../components/library-editor';
export default async function Page({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  return <LibraryEditor id={id} />;
}
