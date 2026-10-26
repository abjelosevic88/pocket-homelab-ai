import { PageHead } from '../components/PageHead'
import { Snippets } from '../components/Snippets'

export default function SnippetsPage({ onMenu }: { onMenu?: () => void }) {
  return <div className="page">
    <PageHead title="Snippets" icon="bookmark" onMenu={onMenu} sub="Reusable text for your chats. Open Snippets in the composer to insert one into your draft." />
    <div className="page-scroll"><div className="page-inner"><Snippets /></div></div>
  </div>
}
