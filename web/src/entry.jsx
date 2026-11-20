import React from 'react';
import {createRoot} from 'react-dom/client';
import App, {browserServer} from './main';

// Ask the server before the first page needs the answer.
browserServer.start();

createRoot(document.getElementById('root')).render(<App/>);
