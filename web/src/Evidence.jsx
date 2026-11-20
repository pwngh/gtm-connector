import React from 'react';
import {MotionConfig} from 'motion/react';
import {FileText, Info, SlidersHorizontal} from 'lucide-react';
import {fixtureEvidence} from './renewalFixtures.js';
import {Button, Pill} from './WorkspaceControls.jsx';
import EvidenceFlow from './EvidenceFlow.jsx';

export default function Evidence({state, derived:d, evidence, source, setModal, openStep}) {
  const doc=state.document, fixtureDocument=fixtureEvidence[2];
  return <MotionConfig reducedMotion="user"><EvidenceFlow state={state} derived={d} onSource={index=>source(evidence[index])} onDocument={()=>setModal({type:'documentDetail'})} onStep={openStep}/><section className="plain-panel evidence-library"><div className="panel-header"><div><div className="section-kicker">SOURCE LIBRARY</div><h2>The evidence, with its context.</h2></div><Button onClick={()=>setModal({type:'coverage'})}><SlidersHorizontal size={16}/> Sources checked</Button></div>{evidence.map(e=><article className="library-row" key={e.id}><span className="source-icon"><e.icon size={22}/></span><div><div className="library-meta"><Pill>{e.id}</Pill><span>{e.type} · {e.time}</span></div><h3>{e.quote}</h3><p>{e.detail}</p><span className="muted">{e.role} · {e.agreement}</span></div><Button ghost onClick={()=>source(e)}>Inspect source</Button></article>)}{doc&&<article className="library-row"><span className="source-icon"><FileText size={22}/></span><div><Pill tone="green">DOCUMENT</Pill><h3>{doc.title||doc.name||`AI Terms · ${fixtureDocument.version}`}</h3><p>Added through the document collection workflow. {d.isDocumentVerified?'Agreement and version verified.':'Verification pending.'}</p></div><Button ghost onClick={()=>setModal({type:'documentDetail'})}>Inspect document</Button></article>}<div className="library-notice"><Info size={17}/><p>This demo uses invented source records. No Salesforce, Slack or document account is connected.</p></div></section></MotionConfig>;
}
