import React, { ChangeEvent } from 'react';
import { MyVariableQuery } from './types';
import {
  normalizeVariableQuery,
  normalizeVariableQueryTimeout,
  variableQueryDefinition,
} from './variableQuery';

interface VariableQueryProps {
    query: MyVariableQuery;
    onChange: (query: MyVariableQuery, definition: string) => void;
}

export const VariableQueryEditor: React.FC<VariableQueryProps> = ({ onChange, query }) => {
    const state = normalizeVariableQuery(query);

    const updateQuery = (nextQuery: MyVariableQuery) => {
        const normalized = normalizeVariableQuery(nextQuery);
        onChange(normalized, variableQueryDefinition(normalized));
    };

    const handleTimeOutChange = (event: ChangeEvent<HTMLInputElement>) =>{
        if (/^\d*$/.test(event.target.value)) {
            updateQuery({
            ...state,
                timeOut: String(normalizeVariableQueryTimeout(event.target.value)),
            });
        }
    };

    const handleQueryChange = (event: ChangeEvent<HTMLInputElement>) =>
        updateQuery({
            ...state,
            queryText: event.currentTarget.value,
        });

    return (
        <>
            <div className="gf-form">
                <span className="gf-form-label width-10">Query</span>
                <input
                    name="queryText"
                    className="gf-form-input"
                    onChange={handleQueryChange}
                    value={state.queryText ?? ''}
                />
            </div>
            <div className="gf-form">
                <span className="gf-form-label width-10">Timeout</span>
                <input
                    name="timeOut"
                    className="gf-form-input"
                    onChange={handleTimeOutChange}
                    value={state.timeOut ?? ''}
                />
            </div>

        </>
    );
};
